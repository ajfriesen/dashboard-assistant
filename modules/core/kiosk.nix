# Wayland kiosk: Sway locked to a single fullscreen Chromium, autostarted by
# greetd. Sway (not Cage) because on-screen keyboards need wlr-layer-shell and
# auto-show needs input-method-v2 / text-input-v3 — none of which Cage exposes.
#
# The launcher is state-aware: it asks the daemon (/api/state) which view to
# show — the setup wizard, a reconnect splash, or the live dashboard.
{ pkgs, lib, config, ... }:
let
  dbg = config.dashboard.debug.chromiumRemoteDebugging;
  # Fallback dashboard target if runtime.env is somehow missing. Normal config
  # lives in /var/lib/dashboard/runtime.env (HA_URL=...), written by the daemon.
  defaultUrl = "http://homeassistant.local:8123";
  daemonBase = "http://localhost:8080";

  kioskLauncher = pkgs.writeShellScript "ha-kiosk-launch" ''
    set -eu
    HA_URL="${defaultUrl}"
    if [ -r /var/lib/dashboard/runtime.env ]; then
      # shellcheck disable=SC1091
      . /var/lib/dashboard/runtime.env
    fi

    # Ask the daemon what to display. Fall back to SETUP if it is slow to start,
    # so a race never lands us on a blank screen.
    STATE=$(${lib.getExe pkgs.curl} -s --max-time 2 ${daemonBase}/api/state \
      | ${lib.getExe pkgs.jq} -r '.state' 2>/dev/null || echo SETUP)

    case "$STATE" in
      READY)     URL="$HA_URL" ;;
      RECONNECT) URL="${daemonBase}/waiting" ;;
      *)         URL="${daemonBase}/setup" ;;
    esac

    # Host<->guest clipboard when running under QEMU/SPICE: the vdagent virtio
    # port only exists in a VM, so this is a no-op on real hardware.
    if [ -e /dev/virtio-ports/com.redhat.spice.0 ]; then
      ${pkgs.spice-vdagent}/bin/spice-vdagent || true
    fi

    # DEV-only remote debugging (loopback). set -f keeps the `*` in
    # --remote-allow-origins from being glob-expanded during word splitting.
    set -f
    DEBUG_FLAGS="${lib.optionalString dbg "--remote-debugging-port=9222 --remote-allow-origins=*"}"

    exec ${lib.getExe pkgs.chromium} \
      --app="$URL" \
      --no-first-run \
      --disable-infobars \
      --disable-pinch \
      --disable-session-crashed-bubble \
      --overscroll-history-navigation=0 \
      --touch-events=enabled \
      --ozone-platform=wayland \
      --enable-features=UseOzonePlatform \
      --enable-wayland-ime \
      --wayland-text-input-version=3 \
      $DEBUG_FLAGS
  '';

  # Subscribe to Sway window events and force the browser back out of fullscreen
  # whenever it enters it — so the top-layer on-screen keyboard is never hidden.
  # Disabling fullscreen emits a fullscreen_mode:0 event, so this doesn't loop.
  keepWindowed = pkgs.writeShellScript "ha-keep-windowed" ''
    ${pkgs.sway}/bin/swaymsg -t subscribe -m '[ "window" ]' | while IFS= read -r _ev; do
      case "$_ev" in
        *'"fullscreen_mode":1'* | *'"fullscreen_mode": 1'*)
          ${pkgs.sway}/bin/swaymsg '[app_id="chrom"] fullscreen disable' >/dev/null 2>&1 || true
          ;;
      esac
    done
  '';

  # A locked-down single-app Sway config: no keybindings (so touch users can't
  # escape the kiosk), no decorations, XWayland off (Chromium is Wayland-native).
  swayConfig = pkgs.writeText "ha-kiosk-sway.conf" ''
    output * bg #101520 solid_color
    default_border none
    default_floating_border none
    xwayland disable
    seat * hide_cursor 5000

    # squeekboard draws on the "top" layer, which Sway hides beneath a
    # fullscreen window (droidian/squeekboard#6). Chromium's --app windows use
    # app_id "chrome-<url>-Default", so match "chrom" broadly. This catches the
    # fullscreen-at-map case; the watcher below catches later requests.
    for_window [app_id="chrom"] fullscreen disable, border none

    # Belt-and-suspenders: some Chromium/HA fullscreen requests arrive *after*
    # the window maps, which for_window can't catch — so watch window events and
    # immediately un-fullscreen the browser whenever it goes fullscreen. Keeps
    # it a tiled screen-filling window so the top-layer OSK stays visible.
    exec ${keepWindowed}

    # On-screen keyboard: squeekboard auto-shows/hides on text-field focus via
    # input-method-v2 (a layer-shell surface, hence Sway not Cage). It only pops
    # when Chromium advertises text-input — see --enable-wayland-ime above.
    exec ${pkgs.squeekboard}/bin/squeekboard

    # The dashboard/kiosk browser (--app; fills the single Sway workspace).
    exec ${kioskLauncher}
  '';

  # dbus-run-session gives the whole session a bus so squeekboard can own
  # sm.puri.OSK0; greetd starts it as the kiosk user with no login prompt.
  sessionCommand = "${pkgs.dbus}/bin/dbus-run-session -- ${pkgs.sway}/bin/sway --config ${swayConfig}";
in
{
  programs.sway.enable = true;

  services.greetd = {
    enable = true;
    settings = {
      initial_session = {
        user = "kiosk";
        command = sessionCommand;
      };
      default_session = {
        user = "kiosk";
        command = sessionCommand;
      };
    };
  };

  # Guest clipboard agent for QEMU/SPICE (harmless on bare metal — the launcher
  # only starts spice-vdagent when the vdagent virtio port is present).
  services.spice-vdagentd.enable = true;

  # Session starts after the daemon so /api/state is answerable on first paint.
  systemd.services.greetd.after = [ "ha-dashboard-daemon.service" ];

  # squeekboard available for manual debugging; Sway launches it via config.
  environment.systemPackages = [ pkgs.squeekboard ];

  # Shared state dir: daemon (ha-dashboard) writes, kiosk reads. Both are in the
  # `dashboard` group (see default.nix).
  systemd.tmpfiles.rules = [
    "d /var/lib/dashboard 0775 ha-dashboard dashboard - -"
  ];

  hardware.graphics.enable = true;
}
