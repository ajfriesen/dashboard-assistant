package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Zoom levels, in percent, matching touchkio's range: 25%..400%. 100% is no
// zoom. HA's number entity exposes these in 5% steps (see mqtt.go).
const (
	zoomMin     = 25
	zoomMax     = 400
	zoomDefault = 100
)

// Zoom controls the kiosk browser's page zoom. Like Display it can't reach the
// in-session browser directly, so it writes "zoom <pct>" to a FIFO in the shared
// state dir and an agent inside the Sway session applies it over Chromium's CDP
// port (CSS zoom). See kiosk.nix for that agent. The level is persisted so it
// survives a reboot (there's no readback from the browser), and an observer is
// notified after any change so the MQTT bridge republishes it.
type Zoom struct {
	mu       sync.Mutex
	pct      int
	fifo     string
	observer func()
}

// NewZoom loads the persisted level (default 100%). The FIFO path can be
// overridden for testing off-device.
func NewZoom() *Zoom {
	fifo := zoomFifo
	if v := os.Getenv("DASHBOARD_ZOOM_FIFO"); v != "" {
		fifo = v
	}
	pct := loadZoom()
	return &Zoom{pct: pct, fifo: fifo}
}

// SetObserver registers a callback fired (outside the lock) after any change, so
// the MQTT bridge can republish the level.
func (z *Zoom) SetObserver(f func()) {
	z.mu.Lock()
	z.observer = f
	z.mu.Unlock()
}

// Level reports the last requested zoom percent.
func (z *Zoom) Level() int {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.pct
}

// Set requests an absolute zoom level (25..400), persists it, and drives the
// in-session agent over the FIFO. Writing is non-blocking: if the kiosk session
// isn't up there's no reader, and we report that rather than hang the caller (an
// MQTT command handler) — the persisted value is still restored on next launch.
func (z *Zoom) Set(pct int) error {
	pct = clampZoom(pct)
	z.mu.Lock()
	if err := z.writeCmd("zoom " + strconv.Itoa(pct)); err != nil {
		z.mu.Unlock()
		return err
	}
	z.pct = pct
	obs := z.observer
	z.mu.Unlock()

	// Persist outside the lock; a failed save is non-fatal (the level still
	// applied), so log-and-continue in saveZoom rather than fail the command.
	saveZoom(pct)

	if obs != nil {
		obs()
	}
	return nil
}

// writeCmd sends one command line to the in-session agent over the FIFO.
// O_NONBLOCK so opening a reader-less FIFO fails with ENXIO instead of blocking.
func (z *Zoom) writeCmd(line string) error {
	f, err := os.OpenFile(z.fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("zoom session not ready: %w", err)
	}
	_, werr := f.WriteString(line + "\n")
	f.Close()
	if werr != nil {
		return fmt.Errorf("write zoom fifo: %w", werr)
	}
	return nil
}

func clampZoom(p int) int {
	if p < zoomMin {
		return zoomMin
	}
	if p > zoomMax {
		return zoomMax
	}
	return p
}

// loadZoom reads the persisted level, falling back to the default on a missing
// or unparseable file. A stored value is clamped, so an out-of-range file can't
// push the browser past the limits.
func loadZoom() int {
	b, err := os.ReadFile(zoomFile)
	if err != nil {
		return zoomDefault
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return zoomDefault
	}
	return clampZoom(n)
}

// saveZoom atomically persists the level. Mode 0664: readable by the shared
// `dashboard` group (the kiosk restores it on launch), like runtime.env — the
// zoom level isn't secret. Best-effort: a save error is logged, not returned.
func saveZoom(pct int) {
	tmp := zoomFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pct)+"\n"), 0o664); err != nil {
		fmt.Fprintf(os.Stderr, "zoom: save %s: %v\n", zoomFile, err)
		return
	}
	if err := os.Rename(tmp, zoomFile); err != nil {
		fmt.Fprintf(os.Stderr, "zoom: save %s: %v\n", zoomFile, err)
	}
}
