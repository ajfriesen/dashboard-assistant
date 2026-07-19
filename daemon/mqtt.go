package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MQTTConfig is read from the environment. If Broker is empty the bridge is
// disabled, so MQTT is fully opt-in and the daemon runs unchanged without it.
type MQTTConfig struct {
	Broker          string // e.g. tcp://192.168.1.10:1883
	Username        string
	Password        string
	NodeID          string // stable per-device id; namespaces topics + unique_id
	DiscoveryPrefix string // HA MQTT discovery prefix, default "homeassistant"
}

// mqttConfigFromEnv reads the MQTT settings from the environment (as populated
// by the Nix EnvironmentFile). It applies no defaults; loadMQTTConfig merges and
// normalises.
func mqttConfigFromEnv() MQTTConfig {
	return MQTTConfig{
		Broker:          os.Getenv("MQTT_BROKER"),
		Username:        os.Getenv("MQTT_USERNAME"),
		Password:        os.Getenv("MQTT_PASSWORD"),
		NodeID:          os.Getenv("MQTT_NODE_ID"),
		DiscoveryPrefix: os.Getenv("MQTT_DISCOVERY_PREFIX"),
	}
}

// loadMQTTConfig is the effective configuration: the baked-in environment (the
// Nix EnvironmentFile) overlaid by the runtime state file that the web UI and
// config import write, then normalised with defaults. The state file wins
// because it reflects a later, explicit user choice.
func loadMQTTConfig() MQTTConfig {
	cfg := mqttConfigFromEnv()
	if m, err := parseEnvFile(mqttFile); err != nil {
		log.Printf("mqtt: read %s: %v", mqttFile, err)
	} else {
		overlay := func(dst *string, key string) {
			if v, ok := m[key]; ok {
				*dst = v
			}
		}
		overlay(&cfg.Broker, "MQTT_BROKER")
		overlay(&cfg.Username, "MQTT_USERNAME")
		overlay(&cfg.Password, "MQTT_PASSWORD")
		overlay(&cfg.NodeID, "MQTT_NODE_ID")
		overlay(&cfg.DiscoveryPrefix, "MQTT_DISCOVERY_PREFIX")
	}
	return cfg.withDefaults()
}

// withDefaults fills the optional fields: a stable NodeID from the machine-id
// and the standard HA discovery prefix.
func (c MQTTConfig) withDefaults() MQTTConfig {
	if c.NodeID == "" {
		c.NodeID = defaultNodeID()
	}
	if c.DiscoveryPrefix == "" {
		c.DiscoveryPrefix = "homeassistant"
	}
	return c
}

// writeMQTTConfig atomically persists the MQTT settings to the runtime state
// file the daemon reads on start. Only non-empty fields are written. Mode 0640:
// it carries the broker password, so it stays a secret readable by the daemon
// and the shared `dashboard` group — not world-readable like runtime.env.
func writeMQTTConfig(c MQTTConfig) error {
	var b strings.Builder
	writeLine := func(key, val string) {
		if val != "" {
			fmt.Fprintf(&b, "%s=%s\n", key, val)
		}
	}
	writeLine("MQTT_BROKER", c.Broker)
	writeLine("MQTT_USERNAME", c.Username)
	writeLine("MQTT_PASSWORD", c.Password)
	writeLine("MQTT_NODE_ID", c.NodeID)
	writeLine("MQTT_DISCOVERY_PREFIX", c.DiscoveryPrefix)

	tmp := mqttFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, mqttFile)
}

// defaultNodeID derives a stable id from the machine-id (falling back to the
// hostname), so a device keeps the same HA entity across reboots.
func defaultNodeID() string {
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return "hadash_" + id[:min(12, len(id))]
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return "hadash_" + h
	}
	return "hadash"
}

// Bridge publishes the dashboard to Home Assistant over MQTT and applies the
// commands it receives. For now it exposes a single entity: the display, as an
// on/off light.
type Bridge struct {
	cfg    MQTTConfig
	disp   *Display
	client mqtt.Client

	statusTopic    string // availability (LWT)
	cmdTopic       string // HA -> us
	stateTopic     string // us -> HA
	discoveryTopic string // retained discovery config
}

func newBridge(cfg MQTTConfig, disp *Display) *Bridge {
	base := "ha-dashboard/" + cfg.NodeID
	return &Bridge{
		cfg:            cfg,
		disp:           disp,
		statusTopic:    base + "/status",
		cmdTopic:       base + "/display/set",
		stateTopic:     base + "/display/state",
		discoveryTopic: fmt.Sprintf("%s/light/%s/display/config", cfg.DiscoveryPrefix, cfg.NodeID),
	}
}

// start connects and returns; the Paho goroutines keep the link alive in the
// background. Everything that must survive a broker restart (discovery,
// availability, subscription) is done in onConnect so it re-runs on every
// reconnect.
func (b *Bridge) start() {
	opts := mqtt.NewClientOptions().
		AddBroker(b.cfg.Broker).
		SetClientID("ha-dashboard-"+b.cfg.NodeID).
		SetOrderMatters(false).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		// Last will: broker marks us offline if we drop.
		SetBinaryWill(b.statusTopic, []byte("offline"), 1, true).
		SetOnConnectHandler(b.onConnect).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			log.Printf("mqtt: connection lost: %v", err)
		})
	if b.cfg.Username != "" {
		opts.SetUsername(b.cfg.Username)
		opts.SetPassword(b.cfg.Password)
	}

	log.Printf("mqtt: connecting to %s as node %q", b.cfg.Broker, b.cfg.NodeID)
	b.client = mqtt.NewClient(opts)
	// With ConnectRetry the initial Connect() token only completes once the
	// broker is reachable, so wait for it off the caller's goroutine — start()
	// must return promptly (it runs in the HTTP handler and at daemon boot).
	go func() {
		if tok := b.client.Connect(); tok.Wait() && tok.Error() != nil {
			log.Printf("mqtt: initial connect: %v", tok.Error())
		}
	}()
}

// stop marks the device offline (if the link is up) and disconnects. Safe to
// call on a bridge whose initial connect never succeeded.
func (b *Bridge) stop() {
	if b.client == nil {
		return
	}
	if b.client.IsConnectionOpen() {
		b.publish(b.client, b.statusTopic, []byte("offline"), true)
	}
	b.client.Disconnect(250)
	log.Printf("mqtt: disconnected node %q", b.cfg.NodeID)
}

// MQTTManager owns the live bridge and lets the MQTT settings be reconfigured at
// runtime, when the web UI or a config import changes them. Apply is idempotent:
// it tears down any current bridge and starts a fresh one, or leaves MQTT
// disabled when no broker is configured.
type MQTTManager struct {
	mu   sync.Mutex
	disp *Display
	cur  *Bridge
}

func NewMQTTManager(disp *Display) *MQTTManager {
	return &MQTTManager{disp: disp}
}

// Apply (re)starts the bridge with cfg, replacing any running one. An empty
// broker disables MQTT.
func (m *MQTTManager) Apply(cfg MQTTConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cur != nil {
		m.cur.stop()
		m.cur = nil
	}
	if cfg.Broker == "" {
		log.Printf("mqtt: disabled (no broker configured)")
		return
	}
	b := newBridge(cfg, m.disp)
	b.start()
	m.cur = b
}

func (b *Bridge) onConnect(client mqtt.Client) {
	log.Printf("mqtt: connected")

	// HA discovery: describe the display as an on/off light. Retained so HA
	// rediscovers it after its own restart.
	cfg := map[string]any{
		"name":                  "Display",
		"unique_id":             b.cfg.NodeID + "_display",
		"command_topic":         b.cmdTopic,
		"state_topic":           b.stateTopic,
		"payload_on":            "ON",
		"payload_off":           "OFF",
		"availability_topic":    b.statusTopic,
		"payload_available":     "online",
		"payload_not_available": "offline",
		"icon":                  "mdi:monitor",
		"device": map[string]any{
			"identifiers":  []string{b.cfg.NodeID},
			"name":         "HA Dashboard " + b.cfg.NodeID,
			"manufacturer": "ha-dashboard-os",
			"model":        "kiosk",
		},
	}
	payload, _ := json.Marshal(cfg)
	b.publish(client, b.discoveryTopic, payload, true)

	// Announce availability and current state, then listen for commands.
	b.publish(client, b.statusTopic, []byte("online"), true)
	b.publishState(client)

	if tok := client.Subscribe(b.cmdTopic, 1, b.onCommand); tok.Wait() && tok.Error() != nil {
		log.Printf("mqtt: subscribe %s: %v", b.cmdTopic, tok.Error())
	}
}

func (b *Bridge) onCommand(client mqtt.Client, msg mqtt.Message) {
	on := strings.EqualFold(strings.TrimSpace(string(msg.Payload())), "ON")
	if err := b.disp.Set(on); err != nil {
		log.Printf("mqtt: display set on=%v: %v", on, err)
		// Republish actual state so HA's optimistic toggle doesn't drift.
		b.publishState(client)
		return
	}
	b.publishState(client)
}

func (b *Bridge) publishState(client mqtt.Client) {
	state := "OFF"
	if b.disp.On() {
		state = "ON"
	}
	b.publish(client, b.stateTopic, []byte(state), true)
}

func (b *Bridge) publish(client mqtt.Client, topic string, payload []byte, retain bool) {
	if tok := client.Publish(topic, 1, retain, payload); tok.Wait() && tok.Error() != nil {
		log.Printf("mqtt: publish %s: %v", topic, tok.Error())
	}
}
