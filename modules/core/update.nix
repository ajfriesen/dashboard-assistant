# OS update reporting. This is the read-only half of the update feature: it
# bakes the release version into the image so the daemon can tell Home Assistant
# which version is "installed", and points the daemon at a release source so it
# can discover the "latest" version. The daemon exposes both as a single HA MQTT
# `update` entity (see daemon/update.go, daemon/mqtt.go).
#
# Performing the update (an Install button + a privileged rebuild unit) is a
# separate, later step — nothing here can change the running system.
{
  config,
  lib,
  version,
  ...
}:
let
  cfg = config.dashboard.update;
in
{
  options.dashboard.update = {
    repo = lib.mkOption {
      type = lib.types.str;
      default = "ajfriesen/ha-dashboard-os";
      description = ''
        owner/repo whose newest release advertises the latest available version.
        The daemon polls <apiBase>/repos/<repo>/releases/latest.
      '';
    };

    apiBase = lib.mkOption {
      type = lib.types.str;
      default = "https://api.github.com";
      example = "https://git.ajfriesen.com/api/v1";
      description = ''
        Base URL of the releases API. GitHub and Gitea share the
        /repos/<repo>/releases/latest shape, so point this at a Gitea instance to
        track self-hosted releases instead of the GitHub mirror.
      '';
    };

    checkInterval = lib.mkOption {
      type = lib.types.str;
      default = "1h";
      example = "30m";
      description = "How often the daemon re-checks for a newer release (a Go duration).";
    };
  };

  config = {
    # Baked-in installed version the daemon reports to HA. World-readable (0444),
    # read at /etc/ha-dashboard/version.
    environment.etc."ha-dashboard/version".text = version;

    # Merges with the daemon service's other environment settings (see daemon.nix).
    systemd.services.ha-dashboard-daemon.environment = {
      UPDATE_REPO = cfg.repo;
      UPDATE_API_BASE = cfg.apiBase;
      UPDATE_CHECK_INTERVAL = cfg.checkInterval;
    };
  };
}
