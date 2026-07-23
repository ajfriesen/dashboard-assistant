# Development-only debugging affordances. Do not enable on a real device image.
{ config, lib, ... }:
let
  cfg = config.dashboardAssistant.debug;
in
{
  options.dashboardAssistant.debug = {
    chromiumRemoteDebugging = lib.mkEnableOption ''
      Chromium remote debugging on 127.0.0.1:9222 (DEV ONLY).

      Drive the kiosk's browser from a host DevTools session over an SSH tunnel
      (ssh -L 9222:localhost:9222). Because it runs in the kiosk page's context you
      can paste a long password/token with the *host* clipboard, or seed an auth
      token into localStorage — no guest clipboard needed. The port binds to
      loopback, so it is only reachable through the tunnel
    '';

    rootAuthorizedKeys = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      example = [
        "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIN3NFv4a2U/X6mxDSxJLLZECuyae7a/ijgjD3Lwz8iy2AAAABHNzaDo= nixos-desktop-2026-07-11-yubikey5"
      ];
      description = ''
        SSH public keys granted root login for VM/field access (DEV ONLY). Key
        auth works under the default PermitRootLogin=prohibit-password, so no
        further sshd changes are needed.
      '';
    };
  };

  config = lib.mkIf (cfg.rootAuthorizedKeys != [ ]) {
    users.users.root.openssh.authorizedKeys.keys = cfg.rootAuthorizedKeys;
  };
}
