# MQTT bridge to Home Assistant. The daemon publishes the dashboard as HA
# entities over MQTT (currently one: the display, as an on/off light) using
# MQTT discovery. It's opt-in: the daemon enables MQTT only when MQTT_BROKER is
# set, which this module supplies via an EnvironmentFile.
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
