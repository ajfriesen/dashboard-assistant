package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
)

// Theme controls the kiosk browser's dark/light appearance, exposed to Home
// Assistant as a switch (ON = dark). Like Zoom it can't reach the in-session
// browser directly, so it writes "theme <dark|light>" to a FIFO in the shared
// state dir and an agent inside the Sway session flips HA's theme over
// Chromium's CDP port (a `settheme` frontend event). See kiosk.nix for that
// agent. The choice is persisted so the MQTT state survives a daemon restart (HA
// itself persists the applied theme server-side), and an observer is notified
// after any change so the MQTT bridge republishes it.
type Theme struct {
	mu       sync.Mutex
	dark     bool
	fifo     string
	observer func()
}

// NewTheme loads the persisted choice (default light). The FIFO path can be
// overridden for testing off-device.
func NewTheme() *Theme {
	fifo := themeFifo
	if v := os.Getenv("DASHBOARD_THEME_FIFO"); v != "" {
		fifo = v
	}
	return &Theme{dark: loadTheme(), fifo: fifo}
}

// SetObserver registers a callback fired (outside the lock) after any change, so
// the MQTT bridge can republish the state.
func (t *Theme) SetObserver(f func()) {
	t.mu.Lock()
	t.observer = f
	t.mu.Unlock()
}

// Dark reports the last requested appearance (true = dark).
func (t *Theme) Dark() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dark
}

// Set requests dark (true) or light (false), persists it, and drives the
// in-session agent over the FIFO. Writing is non-blocking: if the kiosk session
// isn't up there's no reader, and we report that rather than hang the caller (an
// MQTT command handler) — the persisted value is still restored on next launch.
func (t *Theme) Set(dark bool) error {
	mode := "light"
	if dark {
		mode = "dark"
	}
	t.mu.Lock()
	if err := t.writeCmd("theme " + mode); err != nil {
		t.mu.Unlock()
		return err
	}
	t.dark = dark
	obs := t.observer
	t.mu.Unlock()

	// Persist outside the lock; a failed save is non-fatal (the change still
	// applied), so log-and-continue in saveTheme rather than fail the command.
	saveTheme(dark)

	if obs != nil {
		obs()
	}
	return nil
}

// writeCmd sends one command line to the in-session agent over the FIFO.
// O_NONBLOCK so opening a reader-less FIFO fails with ENXIO instead of blocking.
func (t *Theme) writeCmd(line string) error {
	f, err := os.OpenFile(t.fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("theme session not ready: %w", err)
	}
	_, werr := f.WriteString(line + "\n")
	f.Close()
	if werr != nil {
		return fmt.Errorf("write theme fifo: %w", werr)
	}
	return nil
}

// loadTheme reads the persisted choice ("dark"/"light"), defaulting to light on a
// missing or unrecognised file.
func loadTheme() bool {
	b, err := os.ReadFile(themeFile)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "dark"
}

// saveTheme atomically persists the choice. Mode 0664: readable by the shared
// `dashboard` group (the kiosk restores it on launch), like runtime.env — the
// theme isn't secret. Best-effort: a save error is logged, not returned.
func saveTheme(dark bool) {
	mode := "light"
	if dark {
		mode = "dark"
	}
	tmp := themeFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(mode+"\n"), 0o664); err != nil {
		fmt.Fprintf(os.Stderr, "theme: save %s: %v\n", themeFile, err)
		return
	}
	if err := os.Rename(tmp, themeFile); err != nil {
		fmt.Fprintf(os.Stderr, "theme: save %s: %v\n", themeFile, err)
	}
}
