package main

import (
	"fmt"
	"os"
	"strconv"
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
	mu         sync.Mutex
	on         bool
	brightness int // 0..100, the panel backlight/gamma level
	fifo       string
	observer   func() // notified after any state change, to republish over MQTT
}

// NewDisplay assumes the panel is on and at full brightness at boot; the
// in-session agent reports the real state shortly after, correcting the guess.
// The FIFO path can be overridden for testing off-device.
func NewDisplay() *Display {
	fifo := displayFifo
	if v := os.Getenv("DASHBOARD_DISPLAY_FIFO"); v != "" {
		fifo = v
	}
	return &Display{on: true, brightness: 100, fifo: fifo}
}

// SetObserver registers a callback fired (outside the lock) whenever any tracked
// state changes, so the MQTT bridge can republish it. Used to keep HA in sync
// with out-of-band changes reported over the reverse channel.
func (d *Display) SetObserver(f func()) {
	d.mu.Lock()
	d.observer = f
	d.mu.Unlock()
}

// writeCmd sends one command line to the in-session agent over the FIFO.
// O_NONBLOCK so opening a reader-less FIFO fails with ENXIO instead of blocking.
func (d *Display) writeCmd(line string) error {
	f, err := os.OpenFile(d.fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("display session not ready: %w", err)
	}
	_, werr := f.WriteString(line + "\n")
	f.Close()
	if werr != nil {
		return fmt.Errorf("write display fifo: %w", werr)
	}
	return nil
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
		obs()
	}
}

// SetBrightness requests an absolute backlight level (0..100) via the FIFO, the
// brightness analogue of Set. The in-session agent applies it with whichever
// backend it resolved (backlight / DDC / software gamma).
func (d *Display) SetBrightness(pct int) error {
	pct = clampPct(pct)
	d.mu.Lock()
	if err := d.writeCmd("bright " + strconv.Itoa(pct)); err != nil {
		d.mu.Unlock()
		return err
	}
	d.brightness = pct
	obs := d.observer
	d.mu.Unlock()
	if obs != nil {
		obs()
	}
	return nil
}

// ReportBrightness records the actual level reported in-session (Dim/Brighter
// buttons or a startup resync), the brightness analogue of Report.
func (d *Display) ReportBrightness(pct int) {
	d.mu.Lock()
	d.brightness = clampPct(pct)
	obs := d.observer
	d.mu.Unlock()
	if obs != nil {
		obs()
	}
}

// Brightness reports the last known backlight level (0..100).
func (d *Display) Brightness() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.brightness
}

func clampPct(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// Set requests the display power state. Writing is non-blocking: if the kiosk
// session isn't up yet there's no reader on the FIFO, and we report that rather
// than hang the caller (an MQTT command handler).
func (d *Display) Set(on bool) error {
	cmd := "off"
	if on {
		cmd = "on"
	}
	d.mu.Lock()
	if err := d.writeCmd(cmd); err != nil {
		d.mu.Unlock()
		return err
	}
	d.on = on
	obs := d.observer
	d.mu.Unlock()

	// Notify outside the lock: the observer may publish over MQTT (a blocking
	// broker round-trip) and we must not hold the lock across it.
	if obs != nil {
		obs()
	}
	return nil
}

// On reports the last requested state (optimistic; we don't read back from Sway).
func (d *Display) On() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.on
}
