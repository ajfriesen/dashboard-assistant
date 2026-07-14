package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

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

func mqttConfigFromEnv() MQTTConfig {
	cfg := MQTTConfig{
		Broker:          os.Getenv("MQTT_BROKER"),
		Username:        os.Getenv("MQTT_USERNAME"),
		Password:        os.Getenv("MQTT_PASSWORD"),
		NodeID:          os.Getenv("MQTT_NODE_ID"),
		DiscoveryPrefix: os.Getenv("MQTT_DISCOVERY_PREFIX"),
	}
	if cfg.NodeID == "" {
		cfg.NodeID = defaultNodeID()
	}
	if cfg.DiscoveryPrefix == "" {
		cfg.DiscoveryPrefix = "homeassistant"
	}
	return cfg
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
	cfg  MQTTConfig
	disp *Display

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

// run connects and blocks-forever via the Paho auto-reconnect loop. Everything
// that must survive a broker restart (discovery, availability, subscription) is
// done in onConnect so it re-runs on every reconnect.
func (b *Bridge) run() {
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
	client := mqtt.NewClient(opts)
	// With ConnectRetry the first Connect() also retries in the background.
	if tok := client.Connect(); tok.Wait() && tok.Error() != nil {
		log.Printf("mqtt: initial connect: %v", tok.Error())
	}
	select {} // block; the Paho goroutines do the work
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
