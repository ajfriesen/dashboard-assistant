package main

import (
	"fmt"
	"os"
	"sync"
	"syscall"
)

// Display controls the physical panel/monitor power. The daemon runs as
// `ha-dashboard`, but Sway's IPC socket lives under the kiosk user's 0700
// runtime dir, so we can't call swaymsg directly. Instead we write "on"/"off"
// to a FIFO in the shared state dir (group `dashboard`); a small agent running
// *inside* the Sway session reads it and runs `swaymsg output * power on/off`.
// See kiosk.nix for that agent.
type Display struct {
	mu       sync.Mutex
	on       bool
	fifo     string
	observer func(on bool) // notified after any state change, to republish over MQTT
}

// NewDisplay assumes the panel is on at boot (Sway powers outputs on by default);
// the in-session agent reports the real state shortly after, correcting the guess.
// The FIFO path can be overridden for testing off-device.
func NewDisplay() *Display {
	fifo := displayFifo
	if v := os.Getenv("DASHBOARD_DISPLAY_FIFO"); v != "" {
		fifo = v
	}
	return &Display{on: true, fifo: fifo}
}

// SetObserver registers a callback fired (outside the lock) whenever the tracked
// state changes, so the MQTT bridge can republish it. Used to keep HA in sync
// with out-of-band power changes reported over the reverse channel.
func (d *Display) SetObserver(f func(on bool)) {
	d.mu.Lock()
	d.observer = f
	d.mu.Unlock()
}

// Report records the *actual* display power state observed in-session (from the
// Off button, wake-on-touch, or a session restart) — changes that never went
// through Set — and notifies the observer so HA converges to reality. Unlike
// Set it does not drive the FIFO: the change has already happened on the panel.
func (d *Display) Report(on bool) {
	d.mu.Lock()
	d.on = on
	obs := d.observer
	d.mu.Unlock()
	if obs != nil {
		obs(on)
	}
}

// Set requests the display power state. Writing is non-blocking: if the kiosk
// session isn't up yet there's no reader on the FIFO, and we report that rather
// than hang the caller (an MQTT command handler).
func (d *Display) Set(on bool) error {
	d.mu.Lock()

	// O_NONBLOCK so opening a reader-less FIFO fails with ENXIO instead of blocking.
	f, err := os.OpenFile(d.fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		d.mu.Unlock()
		return fmt.Errorf("display session not ready: %w", err)
	}
	cmd := "off\n"
	if on {
		cmd = "on\n"
	}
	_, werr := f.WriteString(cmd)
	f.Close()
	if werr != nil {
		d.mu.Unlock()
		return fmt.Errorf("write display fifo: %w", werr)
	}
	d.on = on
	obs := d.observer
	d.mu.Unlock()

	// Notify outside the lock: the observer may publish over MQTT (a blocking
	// broker round-trip) and we must not hold the lock across it.
	if obs != nil {
		obs(on)
	}
	return nil
}

// On reports the last requested state (optimistic; we don't read back from Sway).
func (d *Display) On() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.on
}
