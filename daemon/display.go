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
	mu   sync.Mutex
	on   bool
	fifo string
}

// NewDisplay assumes the panel is on at boot (Sway powers outputs on by default).
// The FIFO path can be overridden for testing off-device.
func NewDisplay() *Display {
	fifo := displayFifo
	if v := os.Getenv("DASHBOARD_DISPLAY_FIFO"); v != "" {
		fifo = v
	}
	return &Display{on: true, fifo: fifo}
}

// Set requests the display power state. Writing is non-blocking: if the kiosk
// session isn't up yet there's no reader on the FIFO, and we report that rather
// than hang the caller (an MQTT command handler).
func (d *Display) Set(on bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// O_NONBLOCK so opening a reader-less FIFO fails with ENXIO instead of blocking.
	f, err := os.OpenFile(d.fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("display session not ready: %w", err)
	}
	defer f.Close()

	cmd := "off\n"
	if on {
		cmd = "on\n"
	}
	if _, err := f.WriteString(cmd); err != nil {
		return fmt.Errorf("write display fifo: %w", err)
	}
	d.on = on
	return nil
}

// On reports the last requested state (optimistic; we don't read back from Sway).
func (d *Display) On() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.on
}
