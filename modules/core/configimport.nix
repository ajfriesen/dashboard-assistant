# Import dashboard config (HA URL, token, Wi-Fi) from a YAML file, so a device
# can be provisioned without the on-screen wizard — handy for testing and field
# deploys. Two triggers, both feeding the daemon's loopback /api/import:
#
#   1. USB hot-insert: udev fires a per-device service that mounts an inserted
#      USB filesystem read-only and, if it holds dashboard-assistant.yaml, imports it.
#   2. ESP on first boot: /boot is FAT and editable on any computer, so dropping
#      dashboard-assistant.yaml there provisions a freshly flashed image on first boot.
#
# YAML schema (all keys optional):
#   ha_url: "https://homeassistant.local:8123"
#   token: "eyJhbGci..."          # long-lived access token (stored for injection)
#   wifi: { ssid: "Net", psk: "secret" }
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.dashboard.configImport;

  # curl the given YAML file to the daemon. Retries so it works even if the
  # importer races the daemon's startup.
  postToDaemon = file: ''
    ${pkgs.curl}/bin/curl -fsS \
      --retry 10 --retry-connrefused --retry-delay 1 --max-time 30 \
      -H 'Content-Type: application/yaml' \
      --data-binary @${file} http://localhost:8080/api/import
  '';

  usbImport = pkgs.writeShellScript "dashboard-assistant-usb-import" ''
    set -eu
    dev="$1"
    mnt="$(${pkgs.coreutils}/bin/mktemp -d)"
    trap '${pkgs.util-linux}/bin/umount "$mnt" 2>/dev/null || true; ${pkgs.coreutils}/bin/rmdir "$mnt" 2>/dev/null || true' EXIT
    # Read-only mount; bail quietly if it isn't a mountable filesystem.
    ${pkgs.util-linux}/bin/mount -o ro "$dev" "$mnt" 2>/dev/null || exit 0
    [ -f "$mnt/dashboard-assistant.yaml" ] || exit 0
    echo "importing dashboard config from $dev"
    ${postToDaemon "\"$mnt/dashboard-assistant.yaml\""}
  '';

  bootImport = pkgs.writeShellScript "dashboard-assistant-boot-import" ''
    set -eu
    echo "importing dashboard config from /boot/dashboard-assistant.yaml"
    ${postToDaemon "/boot/dashboard-assistant.yaml"}
  '';
in
{
  options.dashboard.configImport.enable = lib.mkEnableOption ''
    importing dashboard config (HA URL, token, Wi-Fi) from a dashboard-assistant.yaml
    on an inserted USB stick, and from the ESP (/boot) on first boot.

    Security note: any USB carrying that file will be applied (this trusts
    physical access). Leave disabled where the ports aren't trusted
  '';

  config = lib.mkIf cfg.enable {
    # USB hot-insert: start the per-device importer for each newly added USB
    # filesystem partition (internal SATA/NVMe disks are not ID_BUS==usb).
    services.udev.extraRules = ''
      ACTION=="add", SUBSYSTEM=="block", ENV{ID_BUS}=="usb", ENV{DEVTYPE}=="partition", ENV{ID_FS_USAGE}=="filesystem", TAG+="systemd", ENV{SYSTEMD_WANTS}+="dashboard-assistant-usb-import@%k.service"
    '';

    systemd.services."dashboard-assistant-usb-import@" = {
      description = "Import dashboard config from USB /dev/%I";
      after = [ "dashboard-assistant-daemon.service" ];
      serviceConfig = {
        Type = "oneshot";
        ExecStart = "${usbImport} /dev/%I";
      };
    };

    # ESP drop-in: only while unprovisioned, so it provisions a fresh image once
    # rather than re-importing on every boot.
    systemd.services.dashboard-assistant-boot-import = {
      description = "Import dashboard config from /boot on first boot";
      wantedBy = [ "multi-user.target" ];
      after = [ "dashboard-assistant-daemon.service" ];
      unitConfig.ConditionPathExists = [
        "/boot/dashboard-assistant.yaml"
        "!/var/lib/dashboard-assistant/provisioned"
      ];
      serviceConfig = {
        Type = "oneshot";
        ExecStart = "${bootImport}";
      };
    };
  };
}
