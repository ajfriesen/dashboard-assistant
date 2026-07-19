# MQTT bridge to Home Assistant. The daemon publishes the dashboard as HA
# entities over MQTT (currently one: the display, as an on/off light) using
# MQTT discovery. It's opt-in: the daemon enables MQTT only when a broker is
# set.
#
# There are two ways to configure it, and the daemon merges them:
#   1. This module's EnvironmentFile — a declarative baseline baked at flash time.
#   2. The runtime state file /var/lib/dashboard/mqtt.env, written by the setup
#      web UI (MQTT tab) and by config import (the `mqtt:` YAML block). This
#      *overrides* the EnvironmentFile, since it reflects a later user choice.
# So this option is optional: MQTT can be set up entirely from the UI / a USB
# bundle after flashing, with no rebuild.
#
# Credentials live in a file *outside* the Nix store (which is world-readable),
# the same reason the HA token isn't baked into the store. See daemon/mqtt.go.
{ config, lib, ... }:
let
  cfg = config.dashboard.mqtt;
in
{
  options.dashboard.mqtt.environmentFile = lib.mkOption {
    type = lib.types.nullOr lib.types.path;
    default = null;
    example = "/var/lib/dashboard/mqtt.env";
    description = ''
      Path to a systemd EnvironmentFile with the MQTT settings the daemon reads.
      Leave null to keep MQTT disabled. Recognised keys:

        MQTT_BROKER=tcp://192.168.1.10:1883   # required to enable the bridge
        MQTT_USERNAME=dashboard
        MQTT_PASSWORD=secret
        MQTT_NODE_ID=living-room              # optional; defaults to machine-id
        MQTT_DISCOVERY_PREFIX=homeassistant   # optional

      Keep this file off the Nix store (e.g. seeded at flash time or provisioned
      at runtime) so the password stays private.
    '';
  };

  config = lib.mkIf (cfg.environmentFile != null) {
    systemd.services.ha-dashboard-daemon.serviceConfig.EnvironmentFile = cfg.environmentFile;
  };
}
