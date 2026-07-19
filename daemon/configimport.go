package main

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// importConfig is the schema of the ha-dashboard.yaml bundle imported from a USB
// stick or the ESP. Every field is optional; only the ones present are applied.
type importConfig struct {
	HAURL string `yaml:"ha_url"`
	Token string `yaml:"token"`
	WiFi  *struct {
		SSID string `yaml:"ssid"`
		PSK  string `yaml:"psk"`
	} `yaml:"wifi"`
	MQTT *struct {
		Broker          string `yaml:"broker"`
		Username        string `yaml:"username"`
		Password        string `yaml:"password"`
		NodeID          string `yaml:"node_id"`
		DiscoveryPrefix string `yaml:"discovery_prefix"`
	} `yaml:"mqtt"`
}

// applyImport parses a YAML bundle and applies whatever it carries: Wi-Fi first
// (so the box is online), then the token, then the HA URL (which also marks the
// device provisioned). It returns a short summary of what changed, so the caller
// can decide whether to restart the kiosk.
func (s *server) applyImport(data []byte) ([]string, error) {
	var cfg importConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	var applied []string

	if cfg.WiFi != nil && cfg.WiFi.SSID != "" {
		if s.nm == nil {
			return applied, fmt.Errorf("wifi requested but NetworkManager is unavailable")
		}
		if err := s.nm.Provision(cfg.WiFi.SSID, cfg.WiFi.PSK); err != nil {
			return applied, fmt.Errorf("wifi provision: %w", err)
		}
		applied = append(applied, "wifi:"+cfg.WiFi.SSID)
	}

	if cfg.Token != "" {
		if err := writeToken(cfg.Token); err != nil {
			return applied, fmt.Errorf("write token: %w", err)
		}
		applied = append(applied, "token")
	}

	if cfg.HAURL != "" {
		if err := writeHAURL(cfg.HAURL); err != nil {
			return applied, fmt.Errorf("write ha_url: %w", err)
		}
		if err := markProvisioned(); err != nil {
			return applied, fmt.Errorf("mark provisioned: %w", err)
		}
		applied = append(applied, "ha_url:"+cfg.HAURL)
	}

	if cfg.MQTT != nil {
		mc := MQTTConfig{
			Broker:          cfg.MQTT.Broker,
			Username:        cfg.MQTT.Username,
			Password:        cfg.MQTT.Password,
			NodeID:          cfg.MQTT.NodeID,
			DiscoveryPrefix: cfg.MQTT.DiscoveryPrefix,
		}
		if err := writeMQTTConfig(mc); err != nil {
			return applied, fmt.Errorf("write mqtt config: %w", err)
		}
		// Apply live so HA discovers the device without waiting for a restart.
		if s.mqtt != nil {
			s.mqtt.Apply(mc.withDefaults())
		}
		applied = append(applied, "mqtt")
	}

	return applied, nil
}
