# Build-time provisioning seed (development / known-network images).
#
# Setting dashboard.seed.haUrl bakes runtime.env and the `provisioned` marker
# into the image, so first boot skips the setup wizard and goes straight to the
# dashboard. This is Option 3 (pre-seed at flash time) expressed as Nix config.
{ config, lib, ... }:
let
  cfg = config.dashboard.seed;
in
{
  options.dashboard.seed = {
    haUrl = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "https://homeassistant.int.ajfriesen.com";
      description = ''
        If set, pre-seed the Home Assistant URL and mark the device as
        provisioned, bypassing the on-screen setup wizard. Leave null to use
        interactive setup.
      '';
    };
  };

  config = lib.mkIf (cfg.haUrl != null) {
    # f+ = always create/truncate, so rebuilding with a new URL updates it.
    # (The dir itself is created by kiosk.nix.)
    systemd.tmpfiles.rules = [
      "f+ /var/lib/dashboard/runtime.env 0664 ha-dashboard dashboard - HA_URL=${cfg.haUrl}"
      "f+ /var/lib/dashboard/provisioned  0664 ha-dashboard dashboard - 1"
    ];
  };
}
