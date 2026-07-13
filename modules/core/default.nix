# Universal OS settings shared across every hardware target.
{ modulesPath, lib, ... }:
{
  imports = [
    "${modulesPath}/profiles/minimal.nix"
    ./kiosk.nix
    ./daemon.nix
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
    #     "/var/lib/dashboard"
    #   ];
  ];

  # documentation.* now lives in ./cleanup.nix.

  networking.hostName = "ha-dashboard";
  networking.networkmanager.enable = true;
  # The minimal profile disables wireless (wpa_supplicant); NetworkManager owns Wi-Fi.
  networking.wireless.enable = lib.mkForce false;

  time.timeZone = "UTC";

  # Shared group for the state dir under /var/lib/dashboard: the daemon
  # (ha-dashboard) writes runtime.env, the kiosk reads it. See daemon.nix / kiosk.nix.
  users.groups.dashboard = { };

  # Dedicated unprivileged kiosk user. Auto-login onto the VT is handled by
  # services.cage (see kiosk.nix).
  users.users.kiosk = {
    isNormalUser = true;
    description = "Dashboard kiosk";
    extraGroups = [ "video" "input" "dashboard" ];
  };

  # Convenience for field debugging over the network.
  services.openssh.enable = true;

  system.stateVersion = "26.05";
}
