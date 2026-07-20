# OS update: report + apply.
#
# Reporting (all targets): bakes the release version into the image so the daemon
# can tell Home Assistant which version is "installed", and points it at a
# release source to discover the "latest". The daemon exposes both as a single HA
# MQTT `update` entity (see daemon/update.go, daemon/mqtt.go).
#
# Applying (installable targets only — the persistent disk / SD image, not the
# ephemeral ISO): a privileged, root-run `ha-update@<tag>.service` does the
# `nixos-rebuild switch --flake <ref>/<tag>#<attr>`, triggered by the daemon over
# the scoped polkit rule below when HA's Install button is pressed. The flake ref
# and hardware attr are baked in; only the target tag is a runtime instance.
# Safety net: the boot-assessment auto-rollback (disk target) and the manual
# recovery UI let a bad update be reverted.
{
  config,
  lib,
  pkgs,
  version,
  ...
}:
let
  cfg = config.dashboard.update;

  updateScript = pkgs.writeShellScript "ha-update" ''
    set -eu
    ref=''${1:-}
    # Re-validate the tag the daemon passed (it also validates) before splicing
    # it into the flake ref — only safe git-tag characters, no shell metachars.
    case "$ref" in "" | *[!A-Za-z0-9._-]*) echo "invalid ref: $ref" >&2; exit 1 ;; esac
    target="${cfg.flakeRef}/$ref#${cfg.flakeAttr}"
    echo "ha-update: switching to $target"
    exec ${pkgs.nixos-rebuild}/bin/nixos-rebuild switch --flake "$target" --refresh
  '';
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

    installable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Whether this image can apply updates in place. Enables the privileged
        ha-update@ rebuild unit and the HA Install button. Leave off for the
        ephemeral live ISO (a switch there wouldn't persist); on for the
        persistent disk / SD targets.
      '';
    };

    flakeRef = lib.mkOption {
      type = lib.types.str;
      default = "github:${cfg.repo}";
      defaultText = lib.literalExpression ''"github:''${config.dashboard.update.repo}"'';
      example = "git+https://git.ajfriesen.com/ajfriesen/dashboard-assistant";
      description = ''
        Flake reference the update rebuild pulls from. The target release tag is
        appended as `<flakeRef>/<tag>`. Defaults to the GitHub mirror of `repo`.
      '';
    };

    flakeAttr = lib.mkOption {
      type = lib.types.str;
      default = "";
      example = "dashboard-x86-disk";
      description = ''
        nixosConfigurations attribute the update rebuild builds
        (`<flakeRef>/<tag>#<flakeAttr>`). Set per hardware target. Required when
        `installable` is true.
      '';
    };
  };

  config = lib.mkMerge [
    {
      # Flake-based `nixos-rebuild --flake` needs these experimental features.
      nix.settings.experimental-features = [
        "nix-command"
        "flakes"
      ];

      # Baked-in installed version the daemon reports to HA. World-readable
      # (0444), read at /etc/ha-dashboard/version.
      environment.etc."ha-dashboard/version".text = version;

      # Merges with the daemon service's other environment settings (daemon.nix).
      systemd.services.ha-dashboard-daemon.environment = {
        UPDATE_REPO = cfg.repo;
        UPDATE_API_BASE = cfg.apiBase;
        UPDATE_CHECK_INTERVAL = cfg.checkInterval;
        UPDATE_INSTALLABLE = if cfg.installable then "1" else "0";
      };
    }

    (lib.mkIf cfg.installable {
      assertions = [
        {
          assertion = cfg.flakeAttr != "";
          message = "dashboard.update.flakeAttr must be set when dashboard.update.installable is true.";
        }
      ];

      # Update to release %i and switch. Instantiated per tag by the daemon
      # (ha-update@<tag>.service); the script re-validates %i. Runs as root; a
      # long build must not time out.
      systemd.services."ha-update@" = {
        description = "Update HA Dashboard OS to release %i and switch";
        # nixos-rebuild shells out to nix (build/eval) and git (flake fetch).
        path = [
          config.nix.package
          pkgs.git
        ];
        serviceConfig = {
          Type = "oneshot";
          ExecStart = "${updateScript} %i";
          TimeoutStartSec = "infinity";
        };
      };

      # Let the daemon start *only* the ha-update@ units (alongside the greetd /
      # ha-rollback@ grants in daemon.nix). extraConfig is concatenated, so this
      # adds a rule rather than replacing the existing one.
      security.polkit.extraConfig = ''
        polkit.addRule(function(action, subject) {
          if (subject.user == "ha-dashboard" &&
              action.id == "org.freedesktop.systemd1.manage-units") {
            var unit = action.lookup("unit");
            if (unit && unit.indexOf("ha-update@") == 0) return polkit.Result.YES;
          }
        });
      '';
    })
  ];
}
