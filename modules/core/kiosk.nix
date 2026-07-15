# Wayland kiosk: Sway locked to a single fullscreen Chromium, autostarted by
# greetd. Sway (not Cage) because on-screen keyboards need wlr-layer-shell and
# auto-show needs input-method-v2 / text-input-v3 — none of which Cage exposes.
#
# The launcher is state-aware: it asks the daemon (/api/state) which view to
# show — the setup wizard, a reconnect splash, or the live dashboard.
{
  pkgs,
  lib,
  config,
  ...
}:
let
  dbg = config.dashboard.debug.chromiumRemoteDebugging;
  autoLogin = config.dashboard.kiosk.autoLogin;
  # Fallback dashboard target if runtime.env is somehow missing. Normal config
  # lives in /var/lib/dashboard/runtime.env (HA_URL=...), written by the daemon.
  defaultUrl = "http://homeassistant.local:8123";
  daemonBase = "http://localhost:8080";
  # Long-lived HA token staged by the daemon (config import / seed).
  tokenPath = "/var/lib/dashboard/token";
  # Set by the Off button after it DPMS-blanks the display; the wake agent
  # clears it and powers the display back on when input (e.g. a touch) arrives.
  displayOffFlag = "/var/lib/dashboard/display-off";
  # Chromium's CDP endpoint; the port binds to loopback (127.0.0.1) by default.
  # Used both by the dev remote-debug flag and by token auto-login.
  cdpArgs = "--remote-debugging-port=9222 --remote-allow-origins=*";

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

    # Open Chromium's loopback CDP port when either dev remote-debugging is on,
    # or auto-login has a token to inject. set -f keeps the `*` in
    # --remote-allow-origins from glob-expanding during word splitting.
    set -f
    DEBUG_FLAGS="${lib.optionalString dbg cdpArgs}"
    ${lib.optionalString autoLogin ''
      if [ -z "$DEBUG_FLAGS" ] && [ -r ${tokenPath} ]; then
        DEBUG_FLAGS="${cdpArgs}"
      fi
    ''}

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

  # Display power agent: the daemon (running as ha-dashboard) can't reach Sway's
  # IPC socket under the kiosk user's 0700 runtime dir, so it writes "on"/"off"
  # to a shared FIFO and this in-session loop applies it via swaymsg. Backs the
  # MQTT "Display" light entity. The FIFO is created by tmpfiles (see below);
  # the outer sleep just avoids a busy loop if it's briefly missing.
  displayAgent = pkgs.writeShellScript "ha-display-agent" ''
    fifo=/var/lib/dashboard/display.fifo
    while true; do
      while IFS= read -r cmd; do
        case "$cmd" in
          on)  ${pkgs.sway}/bin/swaymsg 'output * power on'  >/dev/null 2>&1 || true ;;
          off) ${pkgs.sway}/bin/swaymsg 'output * power off' >/dev/null 2>&1 || true ;;
        esac
      done < "$fifo"
      ${pkgs.coreutils}/bin/sleep 1
    done
  '';

  # Wake-on-touch: wlroots does not re-power an output on input, so after the Off
  # button DPMS-blanks the display (and drops ${displayOffFlag}), this tails
  # libinput events and powers the display back on at the next input event, then
  # clears the flag. DPMS only blanks output — the touchscreen still emits events.
  # While the display is on (no flag) each event is just a cheap stat, so this
  # doesn't spam swaymsg during normal use. The kiosk user is in the `input`
  # group, so libinput can read /dev/input without root.
  wakeAgent = pkgs.writeShellScript "ha-wake-on-touch" ''
    ${pkgs.libinput}/bin/libinput debug-events 2>/dev/null | while IFS= read -r _ev; do
      if [ -e ${displayOffFlag} ]; then
        ${pkgs.sway}/bin/swaymsg 'output * power on' >/dev/null 2>&1 || true
        ${pkgs.coreutils}/bin/rm -f ${displayOffFlag} 2>/dev/null || true
      fi
    done
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

  # Token auto-login: the productionised `just inject-token`. If a token has been
  # staged (config import / seed), wait for Chromium to load the HA origin over
  # the loopback CDP port, then set localStorage `hassTokens` and navigate to the
  # app root. hassTokens is consumed by the app entrypoint, so we navigate to /
  # (NOT reload — the /auth/authorize login screen ignores it). No token ⇒ no-op.
  tokenInjector = pkgs.writeShellScript "ha-token-inject" ''
    set -u
    if [ ! -r ${tokenPath} ]; then exit 0; fi
    TOKEN=$(${pkgs.coreutils}/bin/cat ${tokenPath})
    if [ -z "$TOKEN" ]; then exit 0; fi

    HA_URL="${defaultUrl}"
    if [ -r /var/lib/dashboard/runtime.env ]; then
      # shellcheck disable=SC1091
      . /var/lib/dashboard/runtime.env
    fi
    # Derive the HA origin (scheme://host[:port]) so we only inject into the HA
    # page — not the daemon's setup/waiting pages on a different origin.
    scheme=''${HA_URL%%://*}
    rest=''${HA_URL#*://}
    origin="$scheme://''${rest%%/*}"

    # Wait (up to ~120s) for a CDP page target on the HA origin.
    ws=""
    i=0
    while [ "$i" -lt 120 ]; do
      ws=$(${lib.getExe pkgs.curl} -s --max-time 2 http://localhost:9222/json 2>/dev/null \
        | ${lib.getExe pkgs.jq} -r --arg o "$origin" \
          '[.[] | select(.type=="page" and (.url | startswith($o)))][0].webSocketDebuggerUrl // empty' \
          2>/dev/null)
      if [ -n "$ws" ]; then break; fi
      i=$((i + 1))
      ${pkgs.coreutils}/bin/sleep 1
    done
    if [ -z "$ws" ]; then exit 0; fi

    # JSON-encode the token so it is a safe JS string literal, then inject once.
    tokjson=$(${lib.getExe pkgs.jq} -cn --arg t "$TOKEN" '$t')
    expr='localStorage.setItem("hassTokens", JSON.stringify({access_token:'"$tokjson"',token_type:"Bearer",expires_in:315360000,expires:Date.now()+315360000000,refresh_token:"",clientId:null,hassUrl:location.origin})); location.replace(location.origin + "/");'
    ${lib.getExe pkgs.jq} -cn --arg e "$expr" '{id:1,method:"Runtime.evaluate",params:{expression:$e}}' \
      | ${pkgs.coreutils}/bin/timeout 5 ${pkgs.websocat}/bin/websocat "$ws" >/dev/null 2>&1 || true
  '';

  # Bottom button bar. waybar is a wlr-layer-shell client on the "bottom" layer
  # with an exclusive zone, so Sway reserves the strip and tiles Chromium into
  # the space above it — no manual splitting. squeekboard stays on the "top"
  # layer, so the OSK still overlays these buttons when it pops. Each button is a
  # custom module whose on-click runs a command as the kiosk user:
  #   Off      → DPMS the display off; the wake agent (above) powers it back on
  #              at the next touch/input event.
  #   Dim      → lower the backlight 10% (brightnessctl, video-group udev rule).
  #   Brighter → raise the backlight 10%.
  waybarConfig = pkgs.writeText "ha-kiosk-waybar.json" ''
    {
      "layer": "bottom",
      "position": "bottom",
      "height": 72,
      "modules-left": [],
      "modules-center": ["custom/off", "custom/dim", "custom/bright"],
      "modules-right": [],
      "custom/off": {
        "format": "⏻  Off",
        "tooltip": false,
        "on-click": "${pkgs.sway}/bin/swaymsg 'output * power off'; ${pkgs.coreutils}/bin/touch ${displayOffFlag}"
      },
      "custom/dim": {
        "format": "🔅  Dim",
        "tooltip": false,
        "on-click": "${pkgs.brightnessctl}/bin/brightnessctl set 10%-"
      },
      "custom/bright": {
        "format": "🔆  Brighter",
        "tooltip": false,
        "on-click": "${pkgs.brightnessctl}/bin/brightnessctl set 10%+"
      }
    }
  '';

  waybarStyle = pkgs.writeText "ha-kiosk-waybar.css" ''
    * {
      font-family: sans-serif;
      font-size: 24px;
      min-height: 0;
    }
    window#waybar {
      background: #101520;
      color: #ffffff;
    }
    #custom-off,
    #custom-dim,
    #custom-bright {
      padding: 0 48px;
      margin: 8px;
      background: #1e2633;
      border-radius: 12px;
    }
    #custom-off:active,
    #custom-dim:active,
    #custom-bright:active {
      background: #33415a;
    }
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

    # Applies display on/off requests from the daemon (MQTT "Display" light).
    exec ${displayAgent}

    # Re-powers the display on the next input event after the Off button blanks it.
    exec ${wakeAgent}

    # Bottom button bar: Off / Dim / Brighter. Reserves an exclusive zone, so
    # Chromium tiles into the remaining space above it.
    exec ${pkgs.waybar}/bin/waybar -c ${waybarConfig} -s ${waybarStyle}
    ${lib.optionalString autoLogin ''
      # Auto-login: inject the staged HA token once the dashboard loads.
      exec ${tokenInjector}''}

    # The dashboard/kiosk browser (--app; fills the single Sway workspace).
    exec ${kioskLauncher}
  '';

  # dbus-run-session gives the whole session a bus so squeekboard can own
  # sm.puri.OSK0; greetd starts it as the kiosk user with no login prompt.
  sessionCommand = "${pkgs.dbus}/bin/dbus-run-session -- ${pkgs.sway}/bin/sway --config ${swayConfig}";
in
{
  options.dashboard.kiosk.autoLogin = lib.mkOption {
    type = lib.types.bool;
    default = true;
    description = ''
      Auto-log the kiosk into Home Assistant with the long-lived token staged at
      ${tokenPath} (from config import / seed). When a token is present the
      browser gets a loopback-only Chromium remote-debug port and an in-session
      helper injects the token (sets localStorage hassTokens, navigates to the
      app root). No token means no port and no-op. Disabling turns off both the
      injection and the loopback debug port.
    '';
  };

  config = {
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
    # brightnessctl backs the Dim/Brighter buttons; waybar draws the button bar.
    environment.systemPackages = [
      pkgs.squeekboard
      pkgs.brightnessctl
      pkgs.waybar
    ];

    # brightnessctl ships a udev rule that grants the `video` group write access
    # to /sys/class/backlight, so the (video-group) kiosk user can change
    # brightness from the Dim/Brighter buttons without root.
    services.udev.packages = [ pkgs.brightnessctl ];

    # Shared state dir: daemon (ha-dashboard) writes, kiosk reads. Both are in the
    # `dashboard` group (see default.nix).
    systemd.tmpfiles.rules = [
      "d /var/lib/dashboard 0775 ha-dashboard dashboard - -"
      # FIFO the daemon writes display on/off to; the in-session agent reads it.
      "p /var/lib/dashboard/display.fifo 0660 ha-dashboard dashboard - -"
    ];

    hardware.graphics.enable = true;
  };
}
