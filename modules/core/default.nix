# Universal OS settings shared across every hardware target.
{ modulesPath, lib, ... }:
{
  imports = [
    "${modulesPath}/profiles/minimal.nix"
    ./kiosk.nix
    ./daemon.nix
    ./mqtt.nix
    ./update.nix
    ./configimport.nix
    ./seed.nix
    ./debug.nix
    ./cleanup.nix
    ./memory.nix

    # Impermanence (deferred / scaffold only). On the live ISO the root is
    # already an ephemeral squashfs+tmpfs overlay, so we do NOT declare
    # `fileSystems."/"` as tmpfs here. For the future on-disk install target,
    # enable the input below and persist:
    #   inputs.impermanence.nixosModules.impermanence
    #   environment.persistence."/persist".directories = [
    #     "/etc/NetworkManager/system-connections"
    #     "/var/log"
    #     "/var/lib/dashboard-assistant"
    #   ];
  ];

  # documentation.* now lives in ./cleanup.nix.

  # One image serves every device, so the hostname is derived at runtime from the
  # primary NIC's MAC (dashboard-assistant-<last-6-hex>). Empty here so NixOS'
  # activation doesn't pin a static name; the service below sets it before the
  # network comes up.
  networking.hostName = "";
  systemd.services.set-hostname-from-mac = {
    description = "Derive hostname from the primary NIC MAC (dashboard-assistant-<mac>)";
    wantedBy = [ "network-pre.target" ];
    before = [ "network-pre.target" ];
    unitConfig.DefaultDependencies = false;
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    # Pure bash builtins + a /proc write, so no package deps. Picks the first
    # physical interface (a `device` symlink filters out lo/veth/etc.).
    script = ''
      mac=""
      for i in /sys/class/net/*; do
        [ -e "$i/device" ] || continue
        read -r a < "$i/address" || continue
        [ -n "$a" ] && { mac="$a"; break; }
      done
      hex="''${mac//:/}"
      suffix="''${hex: -6}"
      name="dashboard-assistant"
      [ -n "$suffix" ] && name="dashboard-assistant-$suffix"
      echo "$name" > /proc/sys/kernel/hostname
    '';
  };
  networking.networkmanager.enable = true;
  # Don't let NM set the transient hostname from DHCP / reverse-DNS — that would
  # override the MAC-derived name from set-hostname-from-mac above (e.g. a stale
  # router lease resurrecting the old "ha-dashboard").
  networking.networkmanager.settings.main.hostname-mode = "none";
  # The minimal profile disables wireless (wpa_supplicant); NetworkManager owns Wi-Fi.
  networking.wireless.enable = lib.mkForce false;

  time.timeZone = "UTC";

  # Shared group for the state dir under /var/lib/dashboard-assistant: the daemon
  # (dashboard-assistant) writes runtime.env, the kiosk reads it. See daemon.nix / kiosk.nix.
  users.groups.dashboard-assistant = { };

  # Rebrand migration: an older build kept state under /var/lib/dashboard. Move
  # it to the new path once (preserving the token / provisioned marker / config)
  # and re-own it, since the daemon user+group were renamed too. cp -an never
  # clobbers anything tmpfiles already created at the new path. Runs after the
  # `users` activation so the new owner exists.
  system.activationScripts.migrateDashboardAssistantState = {
    deps = [ "users" ];
    text = ''
      if [ -d /var/lib/dashboard ]; then
        mkdir -p /var/lib/dashboard-assistant
        cp -an /var/lib/dashboard/. /var/lib/dashboard-assistant/ 2>/dev/null || true
        rm -rf /var/lib/dashboard
        chown -R dashboard-assistant:dashboard-assistant /var/lib/dashboard-assistant
      fi
    '';
  };

  # Dedicated unprivileged kiosk user. Auto-login onto the VT is handled by
  # services.cage (see kiosk.nix).
  users.users.kiosk = {
    isNormalUser = true;
    description = "Dashboard kiosk";
    extraGroups = [
      "video"
      "input"
      "dashboard-assistant"
    ];
  };

  # Convenience for field debugging over the network.
  services.openssh.enable = true;

  system.stateVersion = "26.05";
}
