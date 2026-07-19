# Go management daemon: Nix package + systemd service.
#
# The daemon owns first-boot provisioning, so unlike a stateless stub it needs a
# stable identity in the `networkmanager` group, write access to the shared state
# dir, and a scoped grant to restart the kiosk. See kiosk.nix for the launcher
# that polls /api/state, and default.nix for the shared `dashboard` group.
{ config, pkgs, ... }:
let
  # Root helper that rolls the system profile back to a given generation and
  # reboots into it. Driven by the daemon over systemd (StartUnit) via the scoped
  # polkit rule below; `switch-to-configuration boot` only rewrites the bootloader
  # default (a file on the ESP, since canTouchEfiVariables = false), it does not
  # touch the running system, so it's safe to run and reboot.
  rollbackScript = pkgs.writeShellScript "ha-rollback" ''
    set -eu
    gen=''${1:-}
    case "$gen" in "" | *[!0-9]*) echo "invalid generation: $gen" >&2; exit 1 ;; esac
    link=/nix/var/nix/profiles/system-$gen-link
    if [ ! -e "$link" ]; then echo "no such generation: $gen" >&2; exit 1; fi
    echo "rolling back to generation $gen and rebooting"
    ${config.nix.package}/bin/nix-env -p /nix/var/nix/profiles/system --switch-generation "$gen"
    /nix/var/nix/profiles/system/bin/switch-to-configuration boot
    ${pkgs.systemd}/bin/systemctl reboot
  '';

  # The DMI serial (/sys/class/dmi/id/*_serial) is root-only (0400), so the
  # daemon (ha-dashboard) can't read it. This runs as root via ExecStartPre=+ and
  # stashes a real serial in the state dir for the daemon to expose; junk BIOS
  # placeholders are skipped so the daemon falls back to the machine-id.
  dmiDump = pkgs.writeShellScript "ha-dmi-dump" ''
    serial=""
    for f in product_serial board_serial chassis_serial; do
      v=$(${pkgs.coreutils}/bin/tr -d '\0' < /sys/class/dmi/id/"$f" 2>/dev/null | ${pkgs.coreutils}/bin/tr -d '\n')
      case "$v" in
        "" | "Default string" | "To be filled by O.E.M." | "None" | "Not Specified" | "System Serial Number" | "0") continue ;;
      esac
      serial="$v"; break
    done
    ${pkgs.coreutils}/bin/printf 'SERIAL=%s\n' "$serial" > /var/lib/dashboard/dmi.env
    ${pkgs.coreutils}/bin/chown ha-dashboard:dashboard /var/lib/dashboard/dmi.env
    ${pkgs.coreutils}/bin/chmod 0640 /var/lib/dashboard/dmi.env
  '';

  ha-dashboard-api = pkgs.buildGoModule {
    pname = "ha-dashboard-api";
    version = "0.1.0";
    src = ../../daemon;

    # godbus + paho.mqtt + yaml.v3 + golang.org/x/*. Recompute after changing go.mod/go.sum
    # by setting this to lib.fakeHash and reading the expected hash from the build.
    vendorHash = "sha256-8ZidVTg6aky0IKiQ0upfnp1i+XItaFcBF/1EA9xAF2k=";

    meta.mainProgram = "ha-dashboard-api";
  };
in
{
  # Stable system identity: DynamicUser can't hold a predictable slot in the
  # networkmanager group, which NM's polkit policy keys off of.
  users.users.ha-dashboard = {
    isSystemUser = true;
    group = "dashboard";
    extraGroups = [ "networkmanager" ];
  };

  systemd.services.ha-dashboard-daemon = {
    description = "HA Dashboard management daemon";
    wantedBy = [ "multi-user.target" ];
    after = [ "network.target" "dbus.service" ];
    wants = [ "network.target" ];

    serviceConfig = {
      # Runs as root (the `+` prefix) before the daemon to capture the root-only
      # DMI serial for the hardware-serial sensor.
      ExecStartPre = "+${dmiDump}";
      ExecStart = "${ha-dashboard-api}/bin/ha-dashboard-api";
      Restart = "on-failure";
      RestartSec = 2;

      User = "ha-dashboard";
      Group = "dashboard";

      # Locked down, but the daemon must talk to the system bus (NetworkManager,
      # systemd) and write the shared runtime config the kiosk reads.
      ProtectSystem = "strict";
      ProtectHome = true;
      NoNewPrivileges = true;
      ReadWritePaths = [ "/var/lib/dashboard" ];
    };

    environment.DASHBOARD_ADDR = ":8080";
  };

  # Roll back to generation %i and reboot. Instantiated per generation by the
  # daemon (ha-rollback@<n>.service); the script validates <n>.
  systemd.services."ha-rollback@" = {
    description = "Roll back to NixOS generation %i and reboot";
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${rollbackScript} %i";
    };
  };

  # Let the daemon manage *only* the greetd session unit (the Sway kiosk) and the
  # ha-rollback@ recovery units — nothing else on the system. Restarting greetd
  # relaunches the kiosk; starting ha-rollback@<n> rolls back and reboots.
  security.polkit.extraConfig = ''
    polkit.addRule(function(action, subject) {
      if (subject.user == "ha-dashboard" &&
          action.id == "org.freedesktop.systemd1.manage-units") {
        var unit = action.lookup("unit");
        if (unit == "greetd.service") return polkit.Result.YES;
        if (unit && unit.indexOf("ha-rollback@") == 0) return polkit.Result.YES;
      }
    });
  '';
}
