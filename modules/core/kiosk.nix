# Wayland kiosk: Sway locked to a single fullscreen Chromium, autostarted by
# greetd. Sway (not Cage) because we need wlr-layer-shell (the waybar button
# bar), floating + no_focus window rules (the vboard on-screen keyboard) and
# output power control — none of which Cage exposes.
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
  dbg = config.dashboardAssistant.debug.chromiumRemoteDebugging;
  autoLogin = config.dashboardAssistant.kiosk.autoLogin;
  # On-screen keyboard, packaged from source (not in nixpkgs). See packages/vboard.nix.
  vboard = pkgs.callPackage ../../packages/vboard.nix { };
  # Fallback dashboard target if runtime.env is somehow missing. Normal config
  # lives in /var/lib/dashboard-assistant/runtime.env (HA_URL=...), written by the daemon.
  defaultUrl = "http://homeassistant.local:8123";
  daemonBase = "http://localhost:8080";
  # Long-lived HA token staged by the daemon (config import / seed).
  tokenPath = "/var/lib/dashboard-assistant/token";
  # Set by the Off button after it DPMS-blanks the display; the wake agent
  # clears it and powers the display back on when input (e.g. a touch) arrives.
  displayOffFlag = "/var/lib/dashboard-assistant/display-off";
  # Reverse status channel: the daemon can only track power changes it commands
  # over MQTT, so anything that changes the panel in-session (Off button, wake-on-
  # touch, a session restart) must report the *actual* state here for the daemon
  # to republish — otherwise HA drifts out of sync. Best-effort, non-blocking:
  # timeout guards the rare window where the daemon isn't holding the FIFO open,
  # so reporting never wedges the caller. Read by watchDisplayState in the daemon.
  displayStateFifo = "/var/lib/dashboard-assistant/display-state.fifo";
  # Browser zoom: the daemon writes "zoom <pct>" to this FIFO (backing the MQTT
  # "Zoom" number entity) and persists the chosen level to zoomFile so the kiosk
  # can restore it after a navigation or a session restart. See zoomAgent below.
  zoomFifo = "/var/lib/dashboard-assistant/zoom.fifo";
  zoomFile = "/var/lib/dashboard-assistant/zoom";
  # Dark/light mode: the daemon writes "theme <dark|light>" to this FIFO (backing
  # the MQTT "Dark mode" switch) and persists the choice to themeFile so the kiosk
  # can re-assert it after a navigation or a session restart. See themeAgent below.
  themeFifo = "/var/lib/dashboard-assistant/theme.fifo";
  themeFile = "/var/lib/dashboard-assistant/theme";
  reportDisplayState = pkgs.writeShellScript "ha-report-display-state" ''
    ${pkgs.coreutils}/bin/printf '%s\n' "$*" \
      | ${pkgs.coreutils}/bin/timeout 1 ${pkgs.coreutils}/bin/tee ${displayStateFifo} >/dev/null 2>&1 || true
  '';
  # Chromium's CDP endpoint; the port binds to loopback (127.0.0.1) by default.
  # Used both by the dev remote-debug flag and by token auto-login.
  cdpArgs = "--remote-debugging-port=9222 --remote-allow-origins=*";

  kioskLauncher = pkgs.writeShellScript "ha-kiosk-launch" ''
    set -eu
    HA_URL="${defaultUrl}"
    if [ -r /var/lib/dashboard-assistant/runtime.env ]; then
      # shellcheck disable=SC1091
      . /var/lib/dashboard-assistant/runtime.env
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

    # Chromium refuses to launch when its SingletonLock names a different host
    # ("profile in use by another computer") — which happens once the MAC-derived
    # hostname is assigned, or after an unclean shutdown leaves a stale lock. A
    # kiosk only ever runs one instance, so clearing the guard first is safe.
    # Explicit names (not a glob): `set -f` above disables pathname expansion.
    chromeProfile="''${XDG_CONFIG_HOME:-$HOME/.config}/chromium"
    rm -f "$chromeProfile/SingletonLock" "$chromeProfile/SingletonCookie" "$chromeProfile/SingletonSocket" 2>/dev/null || true

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
      $DEBUG_FLAGS
  '';
  # NB: no --enable-wayland-ime / --wayland-text-input-version here. Those enable
  # the Wayland text-input (IME) protocol, which the old Stevia OSK needed — but
  # on Sway that path is broken: Chromium fires keydown yet never commits the
  # text into the field (swaywm/sway#8276). vboard doesn't use IME anyway — it
  # injects raw key events via /dev/uinput — so leaving these off lets Chromium
  # handle keys natively and the typed characters actually land.

  # Display power agent: the daemon (running as dashboard-assistant) can't reach Sway's
  # IPC socket under the kiosk user's 0700 runtime dir, so it writes "on"/"off"
  # to a shared FIFO and this in-session loop applies it via swaymsg. Backs the
  # MQTT "Display" light entity. The FIFO is created by tmpfiles (see below);
  # the outer sleep just avoids a busy loop if it's briefly missing.
  displayAgent = pkgs.writeShellScript "ha-display-agent" ''
    # Sway does not reap its exec'd children, so a kiosk restart orphans the
    # previous session's agent (pointing at a now-dead Sway socket) and it keeps
    # reading this shared FIFO. Two blocked readers on one FIFO are woken
    # alternately, so the daemon's commands split between them and every other
    # one is silently eaten by the dead agent — the classic "Off works but On
    # doesn't" symptom. Kill any earlier instance so we are the sole reader.
    for pid in $(${pkgs.procps}/bin/pgrep -f ha-display-agent); do
      [ "$pid" = "$$" ] || kill "$pid" 2>/dev/null || true
    done

    # Report the real power state once at startup: a session restart powers
    # outputs back on, so the daemon's optimistic state (and HA) must be resynced.
    init=$(${pkgs.sway}/bin/swaymsg -t get_outputs -r 2>/dev/null \
      | ${lib.getExe pkgs.jq} -r 'if any(.[]; .power) then "on" else "off" end' 2>/dev/null)
    [ -n "$init" ] && ${reportDisplayState} "$init"

    # Hold the FIFO open read-write for the whole session (fd 3). O_RDWR never
    # blocks on open and never sees EOF when a writer disconnects, so a reader is
    # always present and the daemon's non-blocking writes never race against a
    # reopen (which previously caused ENXIO and dropped commands). Same trick the
    # daemon uses for the reverse FIFO.
    exec 3<> /var/lib/dashboard-assistant/display.fifo
    # IFS=' ' (not empty) so the line splits into verb + argument — "bright 40"
    # becomes cmd=bright arg=40; "on"/"off" leave arg empty.
    while IFS=' ' read -r cmd arg <&3; do
      # Apply, then report the actual state back so HA reflects it — this also
      # covers commands that originate outside MQTT. Arm/disarm the wake-on-touch
      # flag here too (not just on the Off button), so an MQTT/HA power-off also
      # lets the next touch re-power the display.
      case "$cmd" in
        on)  ${pkgs.sway}/bin/swaymsg 'output * power on'  >/dev/null 2>&1 || true
             ${pkgs.coreutils}/bin/rm -f ${displayOffFlag} 2>/dev/null || true
             ${reportDisplayState} on  ;;
        off) ${pkgs.sway}/bin/swaymsg 'output * power off' >/dev/null 2>&1 || true
             ${pkgs.coreutils}/bin/touch ${displayOffFlag} 2>/dev/null || true
             ${reportDisplayState} off ;;
        # Absolute backlight level (0..100) from the HA brightness slider.
        bright) ${brightnessSet} "$arg" >/dev/null 2>&1 || true
                ${reportDisplayState} bright "$arg" ;;
      esac
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
    # Same leak as the display agent: kill any orphan from a prior session so we
    # don't stack up a libinput reader per kiosk restart.
    for pid in $(${pkgs.procps}/bin/pgrep -f ha-wake-on-touch); do
      [ "$pid" = "$$" ] || kill "$pid" 2>/dev/null || true
    done

    # Tail libinput. On each real input event (touch/pointer/key — skip device
    # add/remove and other noise) report a touch to the daemon for the "seconds
    # since last touch" sensor, rate-limited to once a second so a drag doesn't
    # flood the FIFO. And if the display was blanked (flag set), power it on.
    last=0
    ${pkgs.libinput}/bin/libinput debug-events 2>/dev/null | while IFS= read -r ev; do
      case "$ev" in
        *TOUCH_*|*POINTER_*|*KEYBOARD_KEY*|*GESTURE_*|*SWITCH_*) ;;
        *) continue ;;
      esac
      printf -v now '%(%s)T' -1
      if [ "$now" != "$last" ]; then
        ${reportDisplayState} touch
        last=$now
      fi
      if [ -e ${displayOffFlag} ]; then
        ${pkgs.sway}/bin/swaymsg 'output * power on' >/dev/null 2>&1 || true
        ${reportDisplayState} on
        ${pkgs.coreutils}/bin/rm -f ${displayOffFlag} 2>/dev/null || true
      fi
    done
  '';

  # Subscribe to Sway window events and force the browser back out of fullscreen
  # whenever it enters it — a fullscreen window would cover the floating OSK, so
  # keeping Chromium windowed keeps the keyboard (and the bottom bar) visible.
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

  # OSK focus guard: vboard is an xdg-shell toplevel, so Sway focuses it on tap —
  # and `no_focus` only blocks auto-focus on map, not click/tap-to-focus. A tap
  # on a key would therefore move keyboard focus to vboard (which has no text
  # field), so the key it emits lands nowhere. This watches focus events and, the
  # instant vboard gains focus, hands keyboard focus straight back to Chromium.
  # The tap's pointer grab stays on vboard (Wayland keeps pointer + keyboard focus
  # separate), so the key still emits — but now into the focused browser. The
  # refocus targets app_id "chrom" (Chromium's --app id) and doesn't re-fire for
  # vboard, so there's no loop.
  oskFocusGuard = pkgs.writeShellScript "ha-osk-focus-guard" ''
    ${pkgs.sway}/bin/swaymsg -t subscribe -m '[ "window" ]' \
      | ${lib.getExe pkgs.jq} --unbuffered -r \
          'select(.change == "focus" and .container.app_id == "${oskAppId}") | "steal"' \
      | while IFS= read -r _; do
          ${pkgs.sway}/bin/swaymsg '[app_id="chrom"] focus' >/dev/null 2>&1 || true
        done
  '';

  # Token auto-login: the productionised `just inject-token`. If a token has been
  # staged (config import / seed), set localStorage `hassTokens` on the HA page
  # over the loopback CDP port and navigate to the app root, which the app
  # entrypoint consumes to log in (navigate to /, NOT reload — the
  # /auth/authorize login screen ignores hassTokens). No token ⇒ no-op.
  #
  # This runs at session start, racing Chromium's cold load: the CDP page target
  # matches the HA origin *before* the real document commits, so an early inject
  # lands on the throwaway initial document and is lost (leaving the tokenless
  # load to settle on the login screen). So we gate on the document actually
  # being on the HA origin, then inject and re-check — retrying until hassTokens
  # sticks and we're off /auth/*.
  tokenInjector = pkgs.writeShellScript "ha-token-inject" ''
    set -u
    if [ ! -r ${tokenPath} ]; then exit 0; fi
    TOKEN=$(${pkgs.coreutils}/bin/cat ${tokenPath})
    if [ -z "$TOKEN" ]; then exit 0; fi

    HA_URL="${defaultUrl}"
    if [ -r /var/lib/dashboard-assistant/runtime.env ]; then
      # shellcheck disable=SC1091
      . /var/lib/dashboard-assistant/runtime.env
    fi
    # Derive the HA origin (scheme://host[:port]) so we only inject into the HA
    # page — not the daemon's setup/waiting pages on a different origin.
    scheme=''${HA_URL%%://*}
    rest=''${HA_URL#*://}
    origin="$scheme://''${rest%%/*}"

    # JSON-encode the token once so it is a safe JS string literal.
    tokjson=$(${lib.getExe pkgs.jq} -cn --arg t "$TOKEN" '$t')
    inject='localStorage.setItem("hassTokens", JSON.stringify({access_token:'"$tokjson"',token_type:"Bearer",expires_in:315360000,expires:Date.now()+315360000000,refresh_token:"",clientId:null,hassUrl:location.origin})); location.replace(location.origin + "/"); "injected"'

    # Evaluate a JS expression on the first HA-origin CDP page and print its
    # value. Empty if there is no such page/target yet.
    cdp_eval() {
      ws=$(${lib.getExe pkgs.curl} -s --max-time 2 http://localhost:9222/json 2>/dev/null \
        | ${lib.getExe pkgs.jq} -r --arg o "$origin" \
          '[.[] | select(.type=="page" and (.url | startswith($o)))][0].webSocketDebuggerUrl // empty' \
          2>/dev/null)
      [ -z "$ws" ] && return 1
      ${lib.getExe pkgs.jq} -cn --arg e "$1" \
          '{id:1,method:"Runtime.evaluate",params:{expression:$e,returnByValue:true}}' \
        | ${pkgs.coreutils}/bin/timeout 5 ${pkgs.websocat}/bin/websocat -n1 "$ws" 2>/dev/null \
        | ${lib.getExe pkgs.jq} -r '.result.result.value // empty' 2>/dev/null
    }

    # Poll for up to ~120s. status = "<on-HA-origin>|<has-tokens>|<on-/auth/>".
    i=0
    while [ "$i" -lt 60 ]; do
      i=$((i + 1))
      st=$(cdp_eval 'String(location.origin==="'"$origin"'"?1:0)+"|"+(localStorage.getItem("hassTokens")?1:0)+"|"+(location.pathname.indexOf("/auth/")===0?1:0)') || st=""
      case "$st" in
        "")                        ${pkgs.coreutils}/bin/sleep 1; continue ;;  # no page yet
        "1|1|0")                   exit 0 ;;                                     # logged in — done
        "1|0|0" | "1|0|1" | "1|1|1") cdp_eval "$inject" >/dev/null ;;           # on origin — (re)inject
        *)                         : ;;                                          # not committed yet — wait
      esac
      ${pkgs.coreutils}/bin/sleep 2
    done
  '';

  # Navigate the already-running kiosk browser to a URL via Chromium's loopback
  # CDP port (same mechanism as the token injector) — no relaunch, just a
  # top-level location change. The port is open whenever autoLogin is on (its
  # default) or dev remote-debugging is enabled; if neither, these are no-ops.
  cdpNav = pkgs.writeShellScript "ha-kiosk-nav" ''
    set -u
    url="$1"
    ws=$(${lib.getExe pkgs.curl} -s --max-time 2 http://localhost:9222/json 2>/dev/null \
      | ${lib.getExe pkgs.jq} -r '[.[] | select(.type=="page")][0].webSocketDebuggerUrl // empty' 2>/dev/null)
    if [ -z "$ws" ]; then exit 0; fi
    urljson=$(${lib.getExe pkgs.jq} -cn --arg u "$url" '$u')
    ${lib.getExe pkgs.jq} -cn --arg e "location.assign($urljson)" \
      '{id:1,method:"Runtime.evaluate",params:{expression:$e}}' \
      | ${pkgs.coreutils}/bin/timeout 5 ${pkgs.websocat}/bin/websocat "$ws" >/dev/null 2>&1 || true
  '';

  # The Home button: resolve the live HA URL (runtime.env, written by the daemon)
  # the same way the launcher does, then navigate there.
  navHome = pkgs.writeShellScript "ha-kiosk-nav-home" ''
    HA_URL="${defaultUrl}"
    if [ -r /var/lib/dashboard-assistant/runtime.env ]; then
      # shellcheck disable=SC1091
      . /var/lib/dashboard-assistant/runtime.env
    fi
    exec ${cdpNav} "$HA_URL"
  '';

  # Navigation agent: the daemon writes a target URL to the nav FIFO — on an MQTT
  # page select / Next / Prev, or the waybar Prev/Next buttons (which POST to the
  # daemon) — and this in-session loop navigates the browser there via cdpNav.
  # Held open read-write (fd 3) for the whole session and de-orphaned on start,
  # exactly like the display agent, so the daemon's writes never race a reopen.
  navAgent = pkgs.writeShellScript "ha-nav-agent" ''
    for pid in $(${pkgs.procps}/bin/pgrep -f ha-nav-agent); do
      [ "$pid" = "$$" ] || kill "$pid" 2>/dev/null || true
    done
    exec 3<> /var/lib/dashboard-assistant/nav.fifo
    while IFS= read -r url <&3; do
      [ -n "$url" ] && ${cdpNav} "$url" >/dev/null 2>&1 || true
      # A full navigation resets CSS zoom and may drop the theme, so re-apply the
      # saved zoom level and dark-mode choice to the freshly loaded page.
      # Backgrounded (they wait for the new document to commit) so they don't
      # stall the next nav command.
      ${zoomRestore} >/dev/null 2>&1 &
      ${themeRestore} >/dev/null 2>&1 &
    done
  '';

  # Apply an absolute browser zoom (percent) to the live HA page via Chromium's
  # loopback CDP port — the same mechanism as cdpNav. CSS `zoom` on the root
  # element scales the whole page; setting it on documentElement (always present)
  # avoids racing document.body during a load. Exits non-zero when there is no
  # page target yet, so callers can retry.
  cdpZoom = pkgs.writeShellScript "ha-kiosk-zoom" ''
    set -u
    pct="''${1:-}"
    case "$pct" in ""|*[!0-9]*) exit 0 ;; esac
    ws=$(${lib.getExe pkgs.curl} -s --max-time 2 http://localhost:9222/json 2>/dev/null \
      | ${lib.getExe pkgs.jq} -r '[.[] | select(.type=="page")][0].webSocketDebuggerUrl // empty' 2>/dev/null)
    if [ -z "$ws" ]; then exit 1; fi
    ${lib.getExe pkgs.jq} -cn --arg e "document.documentElement.style.zoom=String($pct/100)" \
      '{id:1,method:"Runtime.evaluate",params:{expression:$e}}' \
      | ${pkgs.coreutils}/bin/timeout 5 ${pkgs.websocat}/bin/websocat "$ws" >/dev/null 2>&1 || true
  '';

  # Restore the persisted zoom level to the browser. Used both after a navigation
  # (the new document loads at 100%) and once at session start. Waits for a page
  # target to appear, then re-applies a few times so the level sticks past the
  # load. A missing file or 100% (the default) is a no-op — nothing to restore.
  zoomRestore = pkgs.writeShellScript "ha-zoom-restore" ''
    set -u
    [ -r ${zoomFile} ] || exit 0
    z=$(${pkgs.coreutils}/bin/cat ${zoomFile} 2>/dev/null | ${pkgs.coreutils}/bin/tr -d '[:space:]')
    case "$z" in ""|100) exit 0 ;; esac
    applied=0
    i=0
    while [ "$i" -lt 60 ]; do
      i=$((i + 1))
      if ${cdpZoom} "$z"; then
        applied=$((applied + 1))
        # A few applies after the page is up settle it past the load, then stop.
        [ "$applied" -ge 3 ] && exit 0
      fi
      ${pkgs.coreutils}/bin/sleep 1
    done
  '';

  # Zoom agent: the daemon writes "zoom <pct>" to the zoom FIFO (backing the MQTT
  # "Zoom" number entity) and this in-session loop applies it immediately. Held
  # open read-write (fd 3) for the whole session and de-orphaned on start, exactly
  # like the display and nav agents, so the daemon's writes never race a reopen.
  zoomAgent = pkgs.writeShellScript "ha-zoom-agent" ''
    for pid in $(${pkgs.procps}/bin/pgrep -f ha-zoom-agent); do
      [ "$pid" = "$$" ] || kill "$pid" 2>/dev/null || true
    done
    exec 3<> ${zoomFifo}
    while IFS=' ' read -r cmd arg <&3; do
      case "$cmd" in
        zoom) ${cdpZoom} "$arg" >/dev/null 2>&1 || true ;;
      esac
    done
  '';

  # Flip Home Assistant's frontend between dark and light via Chromium's loopback
  # CDP port. HA has no OS-level theme hook we can drive; instead we dispatch its
  # own `settheme` frontend event with {dark:true|false}, which HA applies live
  # and persists to the logged-in user's settings (so it survives reloads). This
  # only touches the theme's dark mode, keeping whatever theme is selected. Exits
  # non-zero until the page and its `hass` object are ready, so callers can retry;
  # a no-op when the theme is already in the requested mode (avoids redundant saves).
  cdpTheme = pkgs.writeShellScript "ha-kiosk-theme" ''
    set -u
    mode="''${1:-}"
    case "$mode" in dark|light) ;; *) exit 0 ;; esac
    dark=false
    [ "$mode" = dark ] && dark=true
    ws=$(${lib.getExe pkgs.curl} -s --max-time 2 http://localhost:9222/json 2>/dev/null \
      | ${lib.getExe pkgs.jq} -r '[.[] | select(.type=="page")][0].webSocketDebuggerUrl // empty' 2>/dev/null)
    if [ -z "$ws" ]; then exit 1; fi
    expr="(function(){var el=document.querySelector('home-assistant');if(!el||!el.hass)return 'wait';if(el.hass.themes&&el.hass.themes.darkMode===$dark)return 'ok';el.dispatchEvent(new CustomEvent('settheme',{detail:{dark:$dark},bubbles:true,composed:true}));return 'ok';})()"
    val=$(${lib.getExe pkgs.jq} -cn --arg e "$expr" \
        '{id:1,method:"Runtime.evaluate",params:{expression:$e,returnByValue:true}}' \
      | ${pkgs.coreutils}/bin/timeout 5 ${pkgs.websocat}/bin/websocat -n1 "$ws" 2>/dev/null \
      | ${lib.getExe pkgs.jq} -r '.result.result.value // empty' 2>/dev/null)
    [ "$val" = ok ]
  '';

  # Re-assert the persisted theme on the browser. Used after a navigation and once
  # at session start. Waits for the page's `hass` object to be ready, then applies.
  # Only "dark" needs restoring — light is HA's default and it remembers the choice
  # itself — so a missing/light file is a no-op.
  themeRestore = pkgs.writeShellScript "ha-theme-restore" ''
    set -u
    [ -r ${themeFile} ] || exit 0
    t=$(${pkgs.coreutils}/bin/cat ${themeFile} 2>/dev/null | ${pkgs.coreutils}/bin/tr -d '[:space:]')
    [ "$t" = dark ] || exit 0
    i=0
    while [ "$i" -lt 60 ]; do
      i=$((i + 1))
      ${cdpTheme} dark && exit 0
      ${pkgs.coreutils}/bin/sleep 1
    done
  '';

  # Theme agent: the daemon writes "theme <dark|light>" to the theme FIFO (backing
  # the MQTT "Dark mode" switch) and this in-session loop applies it immediately.
  # Held open read-write (fd 3) for the whole session and de-orphaned on start,
  # exactly like the display, nav and zoom agents.
  themeAgent = pkgs.writeShellScript "ha-theme-agent" ''
    for pid in $(${pkgs.procps}/bin/pgrep -f ha-theme-agent); do
      [ "$pid" = "$$" ] || kill "$pid" 2>/dev/null || true
    done
    exec 3<> ${themeFifo}
    while IFS=' ' read -r cmd arg <&3; do
      case "$cmd" in
        theme) ${cdpTheme} "$arg" >/dev/null 2>&1 || true ;;
      esac
    done
  '';

  # Waybar Prev/Next: cycle the pushable page list. They POST to the daemon — the
  # single owner of the list and current index — which navigates the browser and
  # republishes the current page to HA, so the bar and the HA select stay in sync.
  pagePrev = pkgs.writeShellScript "ha-page-prev" ''
    ${lib.getExe pkgs.curl} -s --max-time 3 -X POST ${daemonBase}/api/nav \
      -H 'Content-Type: application/json' -d '{"dir":"prev"}' >/dev/null 2>&1 || true
  '';
  pageNext = pkgs.writeShellScript "ha-page-next" ''
    ${lib.getExe pkgs.curl} -s --max-time 3 -X POST ${daemonBase}/api/nav \
      -H 'Content-Type: application/json' -d '{"dir":"next"}' >/dev/null 2>&1 || true
  '';

  # Brightness backend, resolved once per session. There is no single "dim the
  # screen" on Linux — it depends on the panel — so a resolver detects which of
  # three tiers to use and stashes the choice in ${brightnessEnv}; brightnessSet
  # (driven by the HA brightness slider over MQTT) reads that and acts. Detection
  # runs once because ddcutil probing is slow; per-set we only source a tiny file.
  #   backlight → /sys/class/backlight exists (internal eDP/tablet panel):
  #               brightnessctl, via the video-group udev rule.
  #   ddc       → external monitor speaking DDC/CI: ddcutil setvcp 10 (needs
  #               i2c-dev + the i2c group; see hardware.i2c below).
  #   software  → universal fallback: wl-gammarelay-rs dims the rendered output
  #               (not the backlight), so it works on any display / VM.
  # dashboardAssistant.kiosk.brightness.method forces a tier; "auto" (default) detects.
  brightnessMethod = config.dashboardAssistant.kiosk.brightness.method;
  brightnessEnv = "/var/lib/dashboard-assistant/brightness.env";

  brightnessResolve = pkgs.writeShellScript "ha-brightness-resolve" ''
    set -u
    method="${brightnessMethod}"

    detect() {
      case "$1" in
        backlight | ddc | software) echo "$1"; return ;;
      esac
      # auto: a real backlight wins; else DDC/CI on an external panel; else
      # the software dimmer, which always works.
      if ${pkgs.coreutils}/bin/ls /sys/class/backlight/*/brightness >/dev/null 2>&1; then
        echo backlight; return
      fi
      if ${pkgs.ddcutil}/bin/ddcutil detect --brief 2>/dev/null \
        | ${pkgs.gnugrep}/bin/grep -q '^Display'; then
        echo ddc; return
      fi
      echo software
    }

    m=$(detect "$method")

    dev=""
    if [ "$m" = backlight ]; then
      dev=$(${pkgs.coreutils}/bin/basename \
        "$(${pkgs.coreutils}/bin/ls -d /sys/class/backlight/* | ${pkgs.coreutils}/bin/head -n1)")
    fi

    # The software dimmer needs a persistent gamma daemon on the session bus.
    if [ "$m" = software ]; then
      if ! ${pkgs.procps}/bin/pgrep -f wl-gammarelay-rs >/dev/null 2>&1; then
        ${pkgs.wl-gammarelay-rs}/bin/wl-gammarelay-rs run >/dev/null 2>&1 &
      fi
    fi

    ${pkgs.coreutils}/bin/printf 'METHOD=%s\nDEVICE=%s\n' "$m" "$dev" > ${brightnessEnv}

    # Read the current level from the hardware where possible (backlight, DDC);
    # software has no readback so assume full. Report it so the HA brightness
    # slider starts in sync after a session restart.
    init=100
    case "$m" in
      backlight)
        p=$(${pkgs.brightnessctl}/bin/brightnessctl -m -d "$dev" 2>/dev/null \
          | ${pkgs.coreutils}/bin/cut -d, -f4 | ${pkgs.coreutils}/bin/tr -d '%')
        [ -n "$p" ] && init=$p ;;
      ddc)
        # getvcp 10 -t prints: "VCP 10 C <cur> <max>"
        set -- $(${pkgs.ddcutil}/bin/ddcutil getvcp 10 -t 2>/dev/null)
        if [ "''${4:-}" != "" ] && [ "''${5:-0}" -gt 0 ] 2>/dev/null; then
          init=$(( $4 * 100 / $5 ))
        fi ;;
    esac
    ${reportDisplayState} bright "$init"
  '';

  # Set an absolute level (0..100) via whichever tier the resolver picked, and
  # record it. A missing env file defaults to software so a set before the
  # resolver finishes still does something sane rather than erroring.
  brightnessSet = pkgs.writeShellScript "ha-brightness-set" ''
    set -u
    METHOD=software
    DEVICE=""
    if [ -r ${brightnessEnv} ]; then
      # shellcheck disable=SC1091
      . ${brightnessEnv}
    fi
    pct=''${1:-}
    case "$pct" in ""|*[!0-9]*) echo "usage: $0 <0-100>" >&2; exit 2 ;; esac
    [ "$pct" -gt 100 ] && pct=100

    case "$METHOD" in
      backlight)
        ${pkgs.brightnessctl}/bin/brightnessctl -d "$DEVICE" set "''${pct}%" >/dev/null 2>&1 || true ;;
      ddc)
        ${pkgs.ddcutil}/bin/ddcutil setvcp 10 "$pct" >/dev/null 2>&1 || true ;;
      software)
        # wl-gammarelay Brightness is 0..1; floor at 0.10 so a touch-only kiosk
        # never dims to an unrecoverable black.
        if [ "$pct" -lt 10 ]; then
          val="0.10"
        else
          val=$(${pkgs.coreutils}/bin/printf '%d.%02d' $((pct / 100)) $((pct % 100)))
        fi
        ${pkgs.systemd}/bin/busctl --user -- \
          set-property rs.wl-gammarelay / rs.wl.gammarelay Brightness d "$val" >/dev/null 2>&1 || true ;;
    esac
  '';

  # On-screen keyboard toggle, driven by the ⌨ Keyboard button on the bar.
  # vboard has no auto-show and isn't a layer-shell surface, so we manage it by
  # hand: if its window is up, kill it (hide); otherwise launch it and dock it to
  # the bottom of the focused output — full width, ~40% tall, sitting just above
  # the 72px button bar. Sway floats/pins/never-focuses it via the app_id rules
  # in the session config (see swayConfig below); this only handles show/hide and
  # geometry. Detection and close go through Sway's tree so we never have to
  # guess the (wrapped) process name.
  oskAppId = "io.github.archisman-panigrahi.vboard";
  oskToggle = pkgs.writeShellScript "ha-osk-toggle" ''
    set -u
    app='${oskAppId}'
    present=$(${pkgs.sway}/bin/swaymsg -t get_tree \
      | ${lib.getExe pkgs.jq} -r --arg a "$app" \
        '[.. | objects | select(.app_id? == $a)] | length')
    if [ "''${present:-0}" != "0" ]; then
      ${pkgs.sway}/bin/swaymsg "[app_id=\"$app\"] kill" >/dev/null 2>&1 || true
      exit 0
    fi

    ${vboard}/bin/vboard >/dev/null 2>&1 &

    # Wait for the window to map, then size and dock it. Poll briefly; give up
    # quietly if it never appears so a stray tap can't wedge the bar. `bar` is the
    # waybar height (keep in sync with waybarConfig), so the OSK sits just above it.
    bar=50
    i=0
    while [ "$i" -lt 50 ]; do
      i=$((i + 1))
      up=$(${pkgs.sway}/bin/swaymsg -t get_tree \
        | ${lib.getExe pkgs.jq} -r --arg a "$app" \
          '[.. | objects | select(.app_id? == $a)] | length')
      if [ "''${up:-0}" != "0" ]; then
        geom=$(${pkgs.sway}/bin/swaymsg -t get_outputs \
          | ${lib.getExe pkgs.jq} -r \
            '[.[] | select(.focused)][0].rect | "\(.x) \(.y) \(.width) \(.height)"')
        # shellcheck disable=SC2086
        set -- $geom
        ox=$1; oy=$2; ow=$3; oh=$4
        # vboard's keys carry CSS min-heights, so the keyboard has a hard minimum
        # height (5 rows + suggestion bar). Read the height it actually mapped at
        # and never force the window shorter than that, or the bottom key row gets
        # clipped inside the too-short surface. On tall enough outputs the 40%
        # target wins; on small screens the natural height does.
        nh=$(${pkgs.sway}/bin/swaymsg -t get_tree \
          | ${lib.getExe pkgs.jq} -r --arg a "$app" \
            '[.. | objects | select(.app_id? == $a)][0].rect.height // 0')
        kh=$(( oh * 2 / 5 ))
        if [ "''${nh:-0}" -gt "$kh" ]; then kh=$nh; fi
        ky=$(( oy + oh - kh - bar ))
        ${pkgs.sway}/bin/swaymsg \
          "[app_id=\"$app\"] resize set width ''${ow}px height ''${kh}px, move absolute position ''${ox}px ''${ky}px" \
          >/dev/null 2>&1 || true
        exit 0
      fi
      ${pkgs.coreutils}/bin/sleep 0.1
    done
  '';

  # Bottom bar. waybar is a wlr-layer-shell client on the "bottom" layer with an
  # exclusive zone, so Sway reserves the strip and tiles Chromium into the space
  # above it — no manual splitting. The OSK stays on the "top" layer, so it still
  # overlays the bar when it pops. Power and brightness are controlled from Home
  # Assistant (the MQTT Display light), so the bar only carries navigation and the
  # keyboard toggle. Each button is a custom module whose on-click runs a command
  # as the kiosk user.
  waybarConfig = pkgs.writeText "ha-kiosk-waybar.json" ''
    {
      "layer": "bottom",
      "position": "bottom",
      "height": 50,
      "modules-left": ["custom/home", "custom/setup"],
      "modules-center": ["custom/prev", "custom/next"],
      "modules-right": ["custom/kbd"],
      "custom/kbd": {
        "format": "⌨  Keyboard",
        "tooltip": false,
        "on-click": "${oskToggle}"
      },
      "custom/home": {
        "format": "🏠  Home",
        "tooltip": false,
        "on-click": "${navHome}"
      },
      "custom/setup": {
        "format": "⚙  Config",
        "tooltip": false,
        "on-click": "${cdpNav} ${daemonBase}/setup"
      },
      "custom/prev": {
        "format": "◀  Prev",
        "tooltip": false,
        "on-click": "${pagePrev}"
      },
      "custom/next": {
        "format": "Next  ▶",
        "tooltip": false,
        "on-click": "${pageNext}"
      }
    }
  '';

  waybarStyle = pkgs.writeText "ha-kiosk-waybar.css" ''
    * {
      font-family: sans-serif;
      font-size: 18px;
      min-height: 0;
    }
    window#waybar {
      background: #101520;
      color: #ffffff;
    }
    #custom-home,
    #custom-setup,
    #custom-prev,
    #custom-next,
    #custom-kbd {
      padding: 0 16px;
      margin: 5px;
      background: #1e2633;
      border-radius: 10px;
    }
    #custom-home:active,
    #custom-setup:active,
    #custom-prev:active,
    #custom-next:active,
    #custom-kbd:active {
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

    # A fullscreen window covers floating surfaces (the OSK) and the bottom bar,
    # so keep Chromium windowed. Its --app windows use app_id
    # "chrome-<url>-Default", so match "chrom" broadly. This catches the
    # fullscreen-at-map case; the watcher below catches later requests.
    for_window [app_id="chrom"] fullscreen disable, border none

    # Belt-and-suspenders: some Chromium/HA fullscreen requests arrive *after*
    # the window maps, which for_window can't catch — so watch window events and
    # immediately un-fullscreen the browser whenever it goes fullscreen. Keeps
    # it a tiled screen-filling window so the floating OSK stays visible.
    exec ${keepWindowed}

    # Hand keyboard focus back to Chromium whenever a tap on the OSK steals it,
    # so the emitted keystrokes always land in the browser (see oskFocusGuard).
    exec ${oskFocusGuard}

    # On-screen keyboard: vboard, a normal GTK3 window toggled by the ⌨ button on
    # the bar (it has no auto-show). It injects keystrokes via /dev/uinput at the
    # kernel level; a tap on it steals keyboard focus (Sway focuses toplevels on
    # click, which no_focus doesn't prevent), so oskFocusGuard above bounces focus
    # back to the browser and the key lands there. Because vboard is a plain
    # toplevel (not a layer-shell surface), pin it ourselves: float it, keep it
    # sticky and borderless. no_focus still helps by stopping it grabbing focus
    # when it first maps. The toggle script sizes and docks it on launch.
    no_focus [app_id="${oskAppId}"]
    for_window [app_id="${oskAppId}"] floating enable, sticky enable, border none

    # Applies display on/off requests from the daemon (MQTT "Display" light).
    exec ${displayAgent}

    # Navigates the browser when a page is pushed/cycled (MQTT select + Next/Prev,
    # or the waybar Prev/Next buttons).
    exec ${navAgent}

    # Applies browser zoom changes from the MQTT "Zoom" number entity, and
    # restores the persisted level to the browser once it has loaded at startup.
    exec ${zoomAgent}
    exec ${zoomRestore}

    # Applies dark/light changes from the MQTT "Dark mode" switch, and re-asserts
    # a persisted dark theme once the dashboard has loaded at startup.
    exec ${themeAgent}
    exec ${themeRestore}

    # Re-powers the display on the next input event after the Off button blanks it.
    exec ${wakeAgent}

    # Detect the brightness backend once (backlight / DDC / software) and stash
    # it for the Dim/Brighter buttons before the bar can be tapped.
    exec ${brightnessResolve}

    # Bottom button bar: Off / Dim / Brighter. Reserves an exclusive zone, so
    # Chromium tiles into the remaining space above it.
    exec ${pkgs.waybar}/bin/waybar -c ${waybarConfig} -s ${waybarStyle}
    ${lib.optionalString autoLogin ''
      # Auto-login: inject the staged HA token once the dashboard loads.
      exec ${tokenInjector}''}

    # The dashboard/kiosk browser (--app; fills the single Sway workspace).
    exec ${kioskLauncher}
  '';

  # dbus-run-session gives the whole session a bus, which vboard needs to
  # register its Gtk.Application on (and for its tray, if any); greetd starts it
  # as the kiosk user with no login prompt.
  sessionCommand = "${pkgs.dbus}/bin/dbus-run-session -- ${pkgs.sway}/bin/sway --config ${swayConfig}";
in
{
  options.dashboardAssistant.kiosk.autoLogin = lib.mkOption {
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

  options.dashboardAssistant.kiosk.brightness.method = lib.mkOption {
    type = lib.types.enum [
      "auto"
      "backlight"
      "ddc"
      "software"
    ];
    default = "auto";
    description = ''
      Which backend the Dim/Brighter buttons use to change screen brightness.
      "auto" (default) detects it once per session: a real backlight
      (/sys/class/backlight) → brightnessctl; else an external monitor speaking
      DDC/CI → ddcutil; else a software gamma dimmer (wl-gammarelay-rs) that
      dims the rendered output and works anywhere. Force a specific tier when
      auto-detection guesses wrong for a particular panel.
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
    systemd.services.greetd.after = [ "dashboard-assistant-daemon.service" ];

    # vboard (on-screen keyboard) available for manual debugging; the ⌨ bar
    # button toggles it via oskToggle. waybar draws the button bar. The Dim/
    # Brighter buttons pick one of three brightness backends at runtime:
    # brightnessctl (internal backlight), ddcutil (external DDC/CI monitor), or
    # wl-gammarelay-rs (software gamma fallback). See brightnessResolve above.
    environment.systemPackages = [
      vboard
      pkgs.brightnessctl
      pkgs.ddcutil
      pkgs.wl-gammarelay-rs
      pkgs.waybar
    ];

    # brightnessctl ships a udev rule that grants the `video` group write access
    # to /sys/class/backlight, so the (video-group) kiosk user can change
    # brightness from the Dim/Brighter buttons without root.
    services.udev.packages = [ pkgs.brightnessctl ];

    # DDC/CI over I2C for the external-monitor brightness path: loads i2c-dev,
    # creates the `i2c` group, and adds udev rules granting it /dev/i2c-*. The
    # kiosk user joins that group so ddcutil works without root.
    hardware.i2c.enable = true;

    # vboard injects keystrokes through /dev/uinput. hardware.uinput loads the
    # module and installs a udev rule granting the `uinput` group access; the
    # kiosk user joins it so vboard can type without root.
    hardware.uinput.enable = true;
    users.users.kiosk.extraGroups = [
      "i2c"
      "uinput"
    ];

    # Shared state dir: daemon (dashboard-assistant) writes, kiosk reads. Both are in the
    # `dashboard` group (see default.nix).
    systemd.tmpfiles.rules = [
      "d /var/lib/dashboard-assistant 0775 dashboard-assistant dashboard-assistant - -"
      # FIFO the daemon writes display on/off to; the in-session agent reads it.
      "p /var/lib/dashboard-assistant/display.fifo 0660 dashboard-assistant dashboard-assistant - -"
      # Reverse FIFO: the in-session agents report the actual power state; the
      # daemon reads it and republishes over MQTT so HA stays in sync.
      "p /var/lib/dashboard-assistant/display-state.fifo 0660 dashboard-assistant dashboard-assistant - -"
      # Nav FIFO: the daemon writes a target URL; the in-session nav agent reads
      # it and navigates the browser.
      "p /var/lib/dashboard-assistant/nav.fifo 0660 dashboard-assistant dashboard-assistant - -"
      # Zoom FIFO: the daemon writes "zoom <pct>"; the in-session zoom agent reads
      # it and applies CSS zoom over Chromium's CDP port.
      "p /var/lib/dashboard-assistant/zoom.fifo 0660 dashboard-assistant dashboard-assistant - -"
      # Theme FIFO: the daemon writes "theme <dark|light>"; the in-session theme
      # agent reads it and flips HA's frontend theme over Chromium's CDP port.
      "p /var/lib/dashboard-assistant/theme.fifo 0660 dashboard-assistant dashboard-assistant - -"
    ];

    hardware.graphics.enable = true;
  };
}
