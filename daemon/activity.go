package main

import (
	"sync"
	"time"
)

// Activity tracks the last touch/input seen in the kiosk session (reported over
// the reverse FIFO), so the daemon can publish "seconds since last touch" to HA.
// It notifies an observer on each touch so the sensor can reset to 0 immediately;
// a ticker in main republishes the climbing value while idle.
type Activity struct {
	mu       sync.Mutex
	last     time.Time
	observer func()
}

// NewActivity seeds the last-touch time to now, so the count starts at 0 at boot
// (time since start) rather than something meaningless.
func NewActivity() *Activity {
	return &Activity{last: time.Now()}
}

// SetObserver registers a callback fired (outside the lock) on each touch.
func (a *Activity) SetObserver(f func()) {
	a.mu.Lock()
	a.observer = f
	a.mu.Unlock()
}

// Touch records that input just happened.
func (a *Activity) Touch() {
	a.mu.Lock()
	a.last = time.Now()
	obs := a.observer
	a.mu.Unlock()
	if obs != nil {
		obs()
	}
}

// SecondsSince is the whole seconds elapsed since the last touch.
func (a *Activity) SecondsSince() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return int(time.Since(a.last).Seconds())
}
