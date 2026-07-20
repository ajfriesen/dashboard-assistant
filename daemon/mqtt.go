package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
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
	pages  *Pages
	act    *Activity
	upd    *UpdateChecker
	client mqtt.Client

	statusTopic      string // availability (LWT)
	cmdTopic         string // power HA -> us
	stateTopic       string // power us -> HA
	brightCmdTopic   string // brightness HA -> us
	brightStateTopic string // brightness us -> HA
	discoveryTopic   string // retained display discovery config

	// Page list: a select to jump to a page + Next/Prev buttons to cycle.
	pageSelectCmdTopic   string
	pageSelectStateTopic string
	pageNextCmdTopic     string
	pagePrevCmdTopic     string
	pageSelectDiscovery  string
	pageNextDiscovery    string
	pagePrevDiscovery    string

	// "Seconds since last touch" sensor.
	touchStateTopic string
	touchDiscovery  string

	// Memory sensors (absolute MiB).
	memTotalTopic     string
	memUsedTopic      string
	memTotalDiscovery string
	memUsedDiscovery  string

	// Storage sensors (absolute GiB).
	diskTotalTopic     string
	diskUsedTopic      string
	diskTotalDiscovery string
	diskUsedDiscovery  string

	// Count of installed NixOS generations.
	genCountTopic     string
	genCountDiscovery string

	// System update: installed vs latest release, plus the Install command
	// (subscribed only when the image can apply updates).
	updateStateTopic string
	updateCmdTopic   string
	updateDiscovery  string

	// Device info / diagnostics.
	hostnameTopic, hostnameDiscovery string
	ipTopic, ipDiscovery             string
	uptimeTopic, uptimeDiscovery     string
	modelTopic, modelDiscovery       string
	cpuTopic, cpuDiscovery           string
	serialTopic, serialDiscovery     string
}

func newBridge(cfg MQTTConfig, disp *Display, pages *Pages, act *Activity, upd *UpdateChecker) *Bridge {
	base := "ha-dashboard/" + cfg.NodeID
	disco := func(kind, obj string) string {
		return fmt.Sprintf("%s/%s/%s/%s/config", cfg.DiscoveryPrefix, kind, cfg.NodeID, obj)
	}
	return &Bridge{
		cfg:              cfg,
		disp:             disp,
		pages:            pages,
		act:              act,
		upd:              upd,
		statusTopic:      base + "/status",
		cmdTopic:         base + "/display/set",
		stateTopic:       base + "/display/state",
		brightCmdTopic:   base + "/display/brightness/set",
		brightStateTopic: base + "/display/brightness/state",
		discoveryTopic:   disco("light", "display"),

		pageSelectCmdTopic:   base + "/page/set",
		pageSelectStateTopic: base + "/page/state",
		pageNextCmdTopic:     base + "/page/next/set",
		pagePrevCmdTopic:     base + "/page/prev/set",
		pageSelectDiscovery:  disco("select", "page"),
		pageNextDiscovery:    disco("button", "page_next"),
		pagePrevDiscovery:    disco("button", "page_prev"),

		touchStateTopic: base + "/touch/seconds",
		touchDiscovery:  disco("sensor", "last_touch"),

		memTotalTopic:     base + "/mem/total",
		memUsedTopic:      base + "/mem/used",
		memTotalDiscovery: disco("sensor", "mem_total"),
		memUsedDiscovery:  disco("sensor", "mem_used"),

		diskTotalTopic:     base + "/disk/total",
		diskUsedTopic:      base + "/disk/used",
		diskTotalDiscovery: disco("sensor", "disk_total"),
		diskUsedDiscovery:  disco("sensor", "disk_used"),

		genCountTopic:     base + "/generations/count",
		genCountDiscovery: disco("sensor", "generations"),

		updateStateTopic: base + "/update/state",
		updateCmdTopic:   base + "/update/install",
		updateDiscovery:  disco("update", "update"),

		hostnameTopic: base + "/host/hostname", hostnameDiscovery: disco("sensor", "hostname"),
		ipTopic: base + "/host/ip", ipDiscovery: disco("sensor", "ip"),
		uptimeTopic: base + "/host/uptime", uptimeDiscovery: disco("sensor", "uptime"),
		modelTopic: base + "/host/model", modelDiscovery: disco("sensor", "model"),
		cpuTopic: base + "/host/cpu", cpuDiscovery: disco("sensor", "cpu"),
		serialTopic: base + "/host/serial", serialDiscovery: disco("sensor", "serial"),
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
	mu    sync.Mutex
	disp  *Display
	pages *Pages
	act   *Activity
	upd   *UpdateChecker
	cur   *Bridge
}

func NewMQTTManager(disp *Display, pages *Pages, act *Activity, upd *UpdateChecker) *MQTTManager {
	m := &MQTTManager{disp: disp, pages: pages, act: act, upd: upd}
	// Republish whenever the display state changes (including reverse-channel
	// reports), through whichever bridge is currently live.
	disp.SetObserver(func() {
		m.withBridge((*Bridge).publishStateNow)
	})
	// Republish the current page + editable slots whenever the list changes.
	pages.SetObserver(func() {
		m.withBridge(func(b *Bridge) { b.ifConnected(b.publishPages) })
	})
	// Reset the touch sensor to 0 immediately on each touch.
	act.SetObserver(func() {
		m.withBridge(func(b *Bridge) { b.ifConnected(b.publishActivity) })
	})
	// Republish the update entity whenever a newer release is discovered.
	upd.SetObserver(func() {
		m.withBridge(func(b *Bridge) { b.ifConnected(b.publishUpdate) })
	})
	return m
}

// PublishTelemetry republishes the periodic sensors — the touch counter (so it
// climbs while idle) and memory. Called on a ticker.
func (m *MQTTManager) PublishTelemetry() {
	m.withBridge(func(b *Bridge) {
		b.ifConnected(func(c mqtt.Client) {
			b.publishActivity(c)
			b.publishMemory(c)
			b.publishDisk(c)
			b.publishHostDynamic(c)
		})
	})
}

func (m *MQTTManager) withBridge(f func(*Bridge)) {
	m.mu.Lock()
	b := m.cur
	m.mu.Unlock()
	if b != nil {
		f(b)
	}
}

// ifConnected runs a publish only while the link is up (retained-topic publishes
// to a down broker just error-log noise).
func (b *Bridge) ifConnected(f func(mqtt.Client)) {
	if b.client != nil && b.client.IsConnectionOpen() {
		f(b.client)
	}
}

// RepublishPageDiscovery re-announces the page select/buttons — call it after the
// page list changes so HA picks up the new options.
func (m *MQTTManager) RepublishPageDiscovery() {
	m.withBridge(func(b *Bridge) {
		b.ifConnected(func(c mqtt.Client) {
			b.publishPageDiscovery(c)
			b.publishPages(c)
		})
	})
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
	b := newBridge(cfg, m.disp, m.pages, m.act, m.upd)
	b.start()
	m.cur = b
}

func (b *Bridge) onConnect(client mqtt.Client) {
	log.Printf("mqtt: connected")

	// HA discovery: describe the display as a dimmable light — on/off drives DPMS
	// power, brightness (0..100) drives the backlight. Retained so HA rediscovers
	// it after its own restart.
	cfg := map[string]any{
		"name":                     "Display",
		"unique_id":                b.cfg.NodeID + "_display",
		"command_topic":            b.cmdTopic,
		"state_topic":              b.stateTopic,
		"payload_on":               "ON",
		"payload_off":              "OFF",
		"brightness":               true,
		"brightness_command_topic": b.brightCmdTopic,
		"brightness_state_topic":   b.brightStateTopic,
		"brightness_scale":         100,
		"availability_topic":       b.statusTopic,
		"payload_available":        "online",
		"payload_not_available":    "offline",
		"icon":                     "mdi:monitor",
		"device":                   b.device(),
	}
	payload, _ := json.Marshal(cfg)
	b.publish(client, b.discoveryTopic, payload, true)

	// Page select + Next/Prev buttons (their own entities, same device).
	b.publishPageDiscovery(client)

	// "Seconds since last touch" sensor.
	touch, _ := json.Marshal(map[string]any{
		"name":                "Seconds since last touch",
		"unique_id":           b.cfg.NodeID + "_last_touch",
		"state_topic":         b.touchStateTopic,
		"unit_of_measurement": "s",
		"state_class":         "measurement",
		"icon":                "mdi:gesture-tap",
		"availability_topic":  b.statusTopic,
		"device":              b.device(),
	})
	b.publish(client, b.touchDiscovery, touch, true)

	// Data-size sensors (memory in MiB, storage in GiB). Totals are effectively
	// constant; used varies (state_class measurement).
	dataSensor := func(obj, name, stateTopic, unit, icon string, stateClass bool) []byte {
		m := map[string]any{
			"name":                name,
			"unique_id":           b.cfg.NodeID + "_" + obj,
			"state_topic":         stateTopic,
			"unit_of_measurement": unit,
			"device_class":        "data_size",
			"icon":                icon,
			"availability_topic":  b.statusTopic,
			"device":              b.device(),
		}
		if stateClass {
			m["state_class"] = "measurement"
		}
		p, _ := json.Marshal(m)
		return p
	}
	b.publish(client, b.memTotalDiscovery, dataSensor("mem_total", "Memory total", b.memTotalTopic, "MiB", "mdi:memory", false), true)
	b.publish(client, b.memUsedDiscovery, dataSensor("mem_used", "Memory used", b.memUsedTopic, "MiB", "mdi:memory", true), true)
	b.publish(client, b.diskTotalDiscovery, dataSensor("disk_total", "Storage total", b.diskTotalTopic, "GiB", "mdi:harddisk", false), true)
	b.publish(client, b.diskUsedDiscovery, dataSensor("disk_used", "Storage used", b.diskUsedTopic, "GiB", "mdi:harddisk", true), true)

	gens, _ := json.Marshal(map[string]any{
		"name":                "Generations",
		"unique_id":           b.cfg.NodeID + "_generations",
		"state_topic":         b.genCountTopic,
		"unit_of_measurement": "generations",
		"state_class":         "measurement",
		"icon":                "mdi:layers-triple",
		"availability_topic":  b.statusTopic,
		"device":              b.device(),
	})
	b.publish(client, b.genCountDiscovery, gens, true)

	// System update entity: installed vs latest release. The JSON state payload
	// uses HA's native update keys (installed_version, latest_version,
	// release_url, in_progress, …), so no value templates are needed. The Install
	// button (command_topic) is offered only when the image can actually apply
	// updates — otherwise the entity is display-only.
	updateCfg := map[string]any{
		"name":               "System update",
		"unique_id":          b.cfg.NodeID + "_update",
		"state_topic":        b.updateStateTopic,
		"device_class":       "firmware",
		"availability_topic": b.statusTopic,
		"icon":               "mdi:package-up",
		"device":             b.device(),
	}
	if b.upd.Installable() {
		updateCfg["command_topic"] = b.updateCmdTopic
		updateCfg["payload_install"] = "INSTALL"
	}
	update, _ := json.Marshal(updateCfg)
	b.publish(client, b.updateDiscovery, update, true)

	// Device info sensors. Regular sensors, except uptime, which stays in HA's
	// "Diagnostic" category.
	info := func(obj, name, stateTopic, unit, devClass, icon string, diagnostic bool) []byte {
		m := map[string]any{
			"name":               name,
			"unique_id":          b.cfg.NodeID + "_" + obj,
			"state_topic":        stateTopic,
			"availability_topic": b.statusTopic,
			"icon":               icon,
			"device":             b.device(),
		}
		if diagnostic {
			m["entity_category"] = "diagnostic"
		}
		if unit != "" {
			m["unit_of_measurement"] = unit
		}
		if devClass != "" {
			m["device_class"] = devClass
		}
		p, _ := json.Marshal(m)
		return p
	}
	b.publish(client, b.hostnameDiscovery, info("hostname", "Hostname", b.hostnameTopic, "", "", "mdi:server", false), true)
	b.publish(client, b.ipDiscovery, info("ip", "IP address", b.ipTopic, "", "", "mdi:ip-network", false), true)
	b.publish(client, b.uptimeDiscovery, info("uptime", "Uptime", b.uptimeTopic, "s", "duration", "mdi:clock-outline", true), true)
	b.publish(client, b.modelDiscovery, info("model", "Model", b.modelTopic, "", "", "mdi:chip", false), true)
	b.publish(client, b.cpuDiscovery, info("cpu", "CPU usage", b.cpuTopic, "%", "", "mdi:cpu-64-bit", false), true)
	b.publish(client, b.serialDiscovery, info("serial", "Serial number", b.serialTopic, "", "", "mdi:barcode", false), true)

	// Static device info — publish once, retained (changes rarely / never).
	b.publish(client, b.hostnameTopic, []byte(hostname()), true)
	b.publish(client, b.modelTopic, []byte(readModel()), true)
	b.publish(client, b.serialTopic, []byte(readSerial()), true)
	b.publishHostDynamic(client)

	// Announce availability and current state, then listen for commands.
	b.publish(client, b.statusTopic, []byte("online"), true)
	b.publishState(client)
	b.publishBrightness(client)
	b.publishPages(client)
	b.publishActivity(client)
	b.publishMemory(client)
	b.publishDisk(client)
	b.publishGenerations(client)
	b.publishUpdate(client)

	subs := []struct {
		topic string
		h     mqtt.MessageHandler
	}{
		{b.cmdTopic, b.onCommand},
		{b.brightCmdTopic, b.onBrightness},
		{b.pageSelectCmdTopic, b.onSelectPage},
		{b.pageNextCmdTopic, b.onNextPage},
		{b.pagePrevCmdTopic, b.onPrevPage},
	}
	if b.upd.Installable() {
		subs = append(subs, struct {
			topic string
			h     mqtt.MessageHandler
		}{b.updateCmdTopic, b.onInstall})
	}
	// Editable "Page N" text slots.
	for i := 0; i < pageSlots; i++ {
		subs = append(subs, struct {
			topic string
			h     mqtt.MessageHandler
		}{b.slotCmdTopic(i), b.onSlot(i)})
	}
	for _, s := range subs {
		if tok := client.Subscribe(s.topic, 1, s.h); tok.Wait() && tok.Error() != nil {
			log.Printf("mqtt: subscribe %s: %v", s.topic, tok.Error())
		}
	}
}

// device is the shared HA device all entities attach to, so the light, page
// select and buttons group under one "HA Dashboard <node>" device.
func (b *Bridge) device() map[string]any {
	return map[string]any{
		"identifiers":  []string{b.cfg.NodeID},
		"name":         "HA Dashboard " + b.cfg.NodeID,
		"manufacturer": "ha-dashboard-os",
		"model":        "kiosk",
	}
}

// Per-slot topics for the editable "Page N" text entities.
func (b *Bridge) slotCmdTopic(i int) string {
	return fmt.Sprintf("ha-dashboard/%s/page/slot/%d/set", b.cfg.NodeID, i)
}
func (b *Bridge) slotStateTopic(i int) string {
	return fmt.Sprintf("ha-dashboard/%s/page/slot/%d", b.cfg.NodeID, i)
}
func (b *Bridge) slotDiscovery(i int) string {
	return fmt.Sprintf("%s/text/%s/page_slot_%d/config", b.cfg.DiscoveryPrefix, b.cfg.NodeID, i)
}

// publishPageDiscovery (re)announces the page entities. The editable "Page N"
// text slots are always present (you edit the list through them, even from
// empty). The navigation select + Next/Prev are only announced when there's at
// least one page — HA requires a select to have options, and cycling nothing is
// pointless — and cleared otherwise.
func (b *Bridge) publishPageDiscovery(client mqtt.Client) {
	// Editable slots (config category): "Page 1".."Page N", each "Name | URL".
	for i := 0; i < pageSlots; i++ {
		slot, _ := json.Marshal(map[string]any{
			"name":               fmt.Sprintf("Page %d", i+1),
			"unique_id":          fmt.Sprintf("%s_page_slot_%d", b.cfg.NodeID, i),
			"command_topic":      b.slotCmdTopic(i),
			"state_topic":        b.slotStateTopic(i),
			"max":                255,
			"entity_category":    "config",
			"availability_topic": b.statusTopic,
			"icon":               "mdi:link-variant",
			"device":             b.device(),
		})
		b.publish(client, b.slotDiscovery(i), slot, true)
	}

	labels := b.pages.Labels()
	if len(labels) == 0 {
		b.publish(client, b.pageSelectDiscovery, nil, true)
		b.publish(client, b.pageNextDiscovery, nil, true)
		b.publish(client, b.pagePrevDiscovery, nil, true)
		return
	}
	sel, _ := json.Marshal(map[string]any{
		"name":               "Page",
		"unique_id":          b.cfg.NodeID + "_page",
		"command_topic":      b.pageSelectCmdTopic,
		"state_topic":        b.pageSelectStateTopic,
		"options":            labels,
		"availability_topic": b.statusTopic,
		"icon":               "mdi:web",
		"device":             b.device(),
	})
	b.publish(client, b.pageSelectDiscovery, sel, true)

	button := func(obj, name, icon, cmd string) []byte {
		p, _ := json.Marshal(map[string]any{
			"name":               name,
			"unique_id":          b.cfg.NodeID + "_" + obj,
			"command_topic":      cmd,
			"availability_topic": b.statusTopic,
			"icon":               icon,
			"device":             b.device(),
		})
		return p
	}
	b.publish(client, b.pageNextDiscovery, button("page_next", "Next page", "mdi:arrow-right-bold", b.pageNextCmdTopic), true)
	b.publish(client, b.pagePrevDiscovery, button("page_prev", "Previous page", "mdi:arrow-left-bold", b.pagePrevCmdTopic), true)
}

// publishPages publishes the current-page state and every slot's value. Used on
// connect and whenever the list changes, so HA's select and "Page N" editors
// both reflect reality (including compaction after an edit).
func (b *Bridge) publishPages(client mqtt.Client) {
	b.publish(client, b.pageSelectStateTopic, []byte(b.pages.CurrentLabel()), true)
	for i := 0; i < pageSlots; i++ {
		b.publish(client, b.slotStateTopic(i), []byte(b.pages.Slot(i)), true)
	}
}

func (b *Bridge) onSelectPage(_ mqtt.Client, msg mqtt.Message) {
	b.pages.Select(strings.TrimSpace(string(msg.Payload())))
}

func (b *Bridge) onNextPage(_ mqtt.Client, _ mqtt.Message) { b.pages.Next() }
func (b *Bridge) onPrevPage(_ mqtt.Client, _ mqtt.Message) { b.pages.Prev() }

// onSlot handles an edit from a "Page N" text entity: apply it, then re-announce
// (options changed) and republish all slots + the current page so HA reflects
// the compacted result.
func (b *Bridge) onSlot(i int) mqtt.MessageHandler {
	return func(client mqtt.Client, msg mqtt.Message) {
		if err := b.pages.SetSlot(i, string(msg.Payload())); err != nil {
			log.Printf("mqtt: set page slot %d: %v", i, err)
		}
		b.publishPageDiscovery(client)
		b.publishPages(client)
	}
}

// publishActivity publishes the seconds-since-last-touch. Not retained: it's a
// live measurement that's always changing, so a retained stale value is noise.
func (b *Bridge) publishActivity(client mqtt.Client) {
	b.publish(client, b.touchStateTopic, []byte(strconv.Itoa(b.act.SecondsSince())), false)
}

// publishMemory publishes total and used physical memory in MiB. Total is
// retained (it's a constant HA can show right after restart); used is not.
func (b *Bridge) publishMemory(client mqtt.Client) {
	total, used, err := readMem()
	if err != nil {
		log.Printf("mqtt: read memory: %v", err)
		return
	}
	b.publish(client, b.memTotalTopic, []byte(strconv.Itoa(total)), true)
	b.publish(client, b.memUsedTopic, []byte(strconv.Itoa(used)), false)
}

// publishDisk publishes total and used root-filesystem space in GiB (one
// decimal). Total is retained (near constant); used is not.
func (b *Bridge) publishDisk(client mqtt.Client) {
	total, used, err := readDisk()
	if err != nil {
		log.Printf("mqtt: read disk: %v", err)
		return
	}
	gib := func(v float64) []byte { return []byte(strconv.FormatFloat(v, 'f', 1, 64)) }
	b.publish(client, b.diskTotalTopic, gib(total), true)
	b.publish(client, b.diskUsedTopic, gib(used), false)
}

// publishGenerations publishes how many NixOS system generations are installed
// (a cheap stat of the profile links). Retained: it changes only on deploy/GC.
func (b *Bridge) publishGenerations(client mqtt.Client) {
	gens, err := listGenerations()
	if err != nil {
		log.Printf("mqtt: list generations: %v", err)
		return
	}
	b.publish(client, b.genCountTopic, []byte(strconv.Itoa(len(gens))), true)
}

// publishUpdate publishes the update entity's JSON state (installed vs latest
// release). Retained: it changes only when a new release lands, and HA should
// have it right after its own restart. Optional fields are omitted when unknown.
func (b *Bridge) publishUpdate(client mqtt.Client) {
	st := b.upd.State()
	payload := map[string]any{
		"installed_version": st.Installed,
		"latest_version":    st.Latest,
		"in_progress":       st.InProgress,
	}
	if st.URL != "" {
		payload["release_url"] = st.URL
	}
	if st.Title != "" {
		payload["title"] = st.Title
	}
	if st.Summary != "" {
		payload["release_summary"] = st.Summary
	}
	p, _ := json.Marshal(payload)
	b.publish(client, b.updateStateTopic, p, true)
}

// onInstall applies the latest release when HA's Install button is pressed. It
// marks the entity in-progress, starts the privileged ha-update@ unit, and
// clears/refreshes state from the job result. A successful switch usually
// restarts the daemon, so the fresh process republishes clean state instead.
func (b *Bridge) onInstall(client mqtt.Client, _ mqtt.Message) {
	tag, ok := b.upd.InstallTarget()
	if !ok {
		log.Printf("mqtt: install requested but no newer release is available")
		b.publishUpdate(client) // reassert current state (clears HA's optimistic spinner)
		return
	}

	b.upd.SetInstalling(true)
	b.publishUpdate(client)
	log.Printf("mqtt: installing update %s", tag)

	err := startUpdate(tag, func(result string) {
		b.upd.SetInstalling(false)
		if result == "done" {
			// Switch may not have restarted us (e.g. only config changed) — re-read
			// the baked-in version so "installed" reflects the new system.
			b.upd.RefreshInstalled()
		} else {
			log.Printf("mqtt: update %s did not complete: %s", tag, result)
		}
		b.ifConnected(b.publishUpdate)
	})
	if err != nil {
		log.Printf("mqtt: start update %s: %v", tag, err)
		b.upd.SetInstalling(false)
		b.publishUpdate(client)
	}
}

// publishHostDynamic publishes the changing device diagnostics: IP, uptime, and
// CPU usage. On the telemetry ticker (and once on connect).
func (b *Bridge) publishHostDynamic(client mqtt.Client) {
	if ip := primaryIP(); ip != "" {
		b.publish(client, b.ipTopic, []byte(ip), true)
	}
	if up, err := readUptime(); err == nil {
		b.publish(client, b.uptimeTopic, []byte(strconv.Itoa(up)), false)
	}
	if pct, err := cpu.Usage(); err == nil {
		b.publish(client, b.cpuTopic, []byte(strconv.Itoa(pct)), false)
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
	// On success Set fires the observer, which publishes the new state.
}

func (b *Bridge) onBrightness(client mqtt.Client, msg mqtt.Message) {
	pct, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
	if err != nil {
		log.Printf("mqtt: bad brightness %q: %v", msg.Payload(), err)
		return
	}
	// A brightness change implies the light is on, so power the display up first
	// (HA sends brightness when you drag the slider from off).
	if !b.disp.On() {
		if err := b.disp.Set(true); err != nil {
			log.Printf("mqtt: display power-on for brightness: %v", err)
		}
	}
	if err := b.disp.SetBrightness(pct); err != nil {
		log.Printf("mqtt: display brightness=%d: %v", pct, err)
		// Republish actual value so HA's optimistic slider doesn't drift.
		b.publishBrightness(client)
		return
	}
	// On success SetBrightness fires the observer, which publishes the new value.
}

// publishStateNow publishes the current display state through this bridge, if it
// is connected. Used by the state observer so both MQTT commands and reverse-
// channel reports converge HA to reality.
func (b *Bridge) publishStateNow() {
	if b.client == nil || !b.client.IsConnectionOpen() {
		return
	}
	b.publishState(b.client)
	b.publishBrightness(b.client)
}

func (b *Bridge) publishState(client mqtt.Client) {
	state := "OFF"
	if b.disp.On() {
		state = "ON"
	}
	b.publish(client, b.stateTopic, []byte(state), true)
}

func (b *Bridge) publishBrightness(client mqtt.Client) {
	b.publish(client, b.brightStateTopic, []byte(strconv.Itoa(b.disp.Brightness())), true)
}

func (b *Bridge) publish(client mqtt.Client, topic string, payload []byte, retain bool) {
	if tok := client.Publish(topic, 1, retain, payload); tok.Wait() && tok.Error() != nil {
		log.Printf("mqtt: publish %s: %v", topic, tok.Error())
	}
}
