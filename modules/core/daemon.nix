# Go management daemon: Nix package + systemd service.
#
# The daemon owns first-boot provisioning, so unlike a stateless stub it needs a
# stable identity in the `networkmanager` group, write access to the shared state
# dir, and a scoped grant to restart the kiosk. See kiosk.nix for the launcher
# that polls /api/state, and default.nix for the shared `dashboard` group.
{ pkgs, ... }:
let
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

  # Let the daemon manage *only* the greetd session unit (the Sway kiosk) —
  # nothing else on the system. Restarting it relaunches the kiosk after setup.
  security.polkit.extraConfig = ''
    polkit.addRule(function(action, subject) {
      if (subject.user == "ha-dashboard" &&
          action.id == "org.freedesktop.systemd1.manage-units" &&
          action.lookup("unit") == "greetd.service") {
        return polkit.Result.YES;
      }
    });
  '';
}
