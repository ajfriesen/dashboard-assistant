package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
)

// State directory layout (shared group `dashboard`; daemon writes, kiosk reads).
// The base dir is overridable via DASHBOARD_STATE_DIR (defaults to the systemd
// StateDirectory); handy for tests and relocating state.
var stateDir = envOr("DASHBOARD_STATE_DIR", "/var/lib/dashboard")

var (
	runtimeEnv  = stateDir + "/runtime.env"
	markerFile  = stateDir + "/provisioned"
	tokenFile   = stateDir + "/token"        // long-lived HA token for kiosk login injection
	displayFifo = stateDir + "/display.fifo" // daemon writes on/off; kiosk agent applies via swaymsg
)

const sessionUnit = "greetd.service" // the Sway kiosk session; restart relaunches it

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Provisioned reports whether the device has ever completed setup. It is the
// sticky bit that separates a fresh device (SETUP) from a set-up-but-offline
// one (RECONNECT) — deliberately independent of live network state.
func Provisioned() bool {
	_, err := os.Stat(markerFile)
	return err == nil
}

// ConfigValid reports whether runtime.env parses and carries a non-empty HA_URL.
func ConfigValid() bool {
	url, err := readHAURL()
	return err == nil && url != ""
}

func readHAURL() (string, error) {
	f, err := os.Open(runtimeEnv)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "HA_URL=") {
			return strings.Trim(strings.TrimPrefix(line, "HA_URL="), `"`), nil
		}
	}
	return "", sc.Err()
}

// writeHAURL atomically rewrites runtime.env with the given dashboard URL.
// Mode 0664 keeps it readable by the shared `dashboard` group (the kiosk user).
func writeHAURL(url string) error {
	tmp := runtimeEnv + ".tmp"
	content := fmt.Sprintf("HA_URL=%s\n", url)
	if err := os.WriteFile(tmp, []byte(content), 0o664); err != nil {
		return err
	}
	return os.Rename(tmp, runtimeEnv)
}

// writeToken atomically stores the long-lived HA access token. Mode 0640: a
// secret, but readable by the shared `dashboard` group (the kiosk that injects
// it), unlike the group-writable runtime.env.
func writeToken(tok string) error {
	tmp := tokenFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, tokenFile)
}

// markProvisioned drops the sticky marker. Also called by the flash-time seed.
func markProvisioned() error {
	return os.WriteFile(markerFile, []byte("1\n"), 0o664)
}

// clearProvisioned removes the marker, so the next launch re-enters SETUP.
func clearProvisioned() error {
	err := os.Remove(markerFile)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// restartKiosk restarts the greetd session over the systemd D-Bus API. A scoped
// polkit rule (see daemon.nix) grants ha-dashboard rights to manage only this
// unit. Restarting re-runs the state-aware launcher, which re-reads /api/state.
func restartKiosk() error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect system bus: %w", err)
	}
	defer conn.Close()

	systemd := conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1")
	var job dbus.ObjectPath
	err = systemd.Call("org.freedesktop.systemd1.Manager.RestartUnit", 0,
		sessionUnit, "replace").Store(&job)
	if err != nil {
		return fmt.Errorf("RestartUnit %s: %w", sessionUnit, err)
	}
	return nil
}

// ensureStateDir makes sure the state directory exists (systemd StateDirectory
// normally handles this; belt-and-suspenders for direct runs).
func ensureStateDir() error {
	return os.MkdirAll(filepath.Dir(runtimeEnv), 0o775)
}
