package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
)

// pageSlots is how many editable "Page N" text entities we expose to HA for
// editing the list (each holds "Name | URL"). A fixed count because MQTT
// discovery entities are static; bump it for more capacity.
const pageSlots = 10

// Page is one entry in the pushable URL list exposed to Home Assistant as a
// select option. Name is the friendly label; URL is what the kiosk navigates to.
type Page struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Label is what HA shows in the select dropdown / current-page state: the name,
// or the URL itself when unnamed.
func (p Page) Label() string {
	if p.Name != "" {
		return p.Name
	}
	return p.URL
}

// Pages holds the configured page list and the index currently pushed to the
// display, and drives the in-session browser over the nav FIFO. Like Display it
// notifies an observer after any change so the MQTT bridge can republish state.
type Pages struct {
	mu       sync.Mutex
	list     []Page
	index    int
	fifo     string
	observer func()
}

// NewPages loads the persisted list. The FIFO path can be overridden for testing.
func NewPages() *Pages {
	fifo := navFifo
	if v := os.Getenv("DASHBOARD_NAV_FIFO"); v != "" {
		fifo = v
	}
	list, err := loadPages()
	if err != nil {
		// Corrupt/unreadable list is non-fatal: start empty, config can rewrite it.
		fmt.Fprintf(os.Stderr, "pages: load %s: %v\n", urlsFile, err)
	}
	return &Pages{list: list, fifo: fifo}
}

// SetObserver registers a callback fired (outside the lock) after any change, so
// the MQTT bridge can republish the current page.
func (p *Pages) SetObserver(f func()) {
	p.mu.Lock()
	p.observer = f
	p.mu.Unlock()
}

// Labels returns the select options in list order.
func (p *Pages) Labels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.list))
	for i, pg := range p.list {
		out[i] = pg.Label()
	}
	return out
}

// CurrentLabel is the label of the page last pushed (empty when the list is).
func (p *Pages) CurrentLabel() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.list) == 0 {
		return ""
	}
	return p.list[p.index].Label()
}

// Slot renders slot i for the HA text editor: "Name | URL" (or just the URL
// when unnamed), or "" for a slot past the end of the list.
func (p *Pages) Slot(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.list) {
		return ""
	}
	pg := p.list[i]
	if pg.Name != "" {
		return pg.Name + " | " + pg.URL
	}
	return pg.URL
}

// SetSlot applies an edit from the HA "Page N" text entity. The value is
// "Name | URL" (name optional; a bare string is the URL). An empty/URL-less
// value clears that slot and compacts the list; otherwise the slot is replaced,
// or appended when it's the first empty slot past the end.
func (p *Pages) SetSlot(i int, value string) error {
	name, url := parseSlot(value)

	p.mu.Lock()
	list := append([]Page(nil), p.list...)
	p.mu.Unlock()

	switch {
	case url == "":
		if i >= 0 && i < len(list) {
			list = append(list[:i], list[i+1:]...) // remove + compact
		}
	case i >= 0 && i < len(list):
		list[i] = Page{Name: name, URL: url}
	default:
		list = append(list, Page{Name: name, URL: url}) // append at the end
	}
	return p.SetList(list)
}

// parseSlot splits "Name | URL" into its parts; a bare value is the URL.
func parseSlot(v string) (name, url string) {
	if before, after, ok := strings.Cut(strings.TrimSpace(v), "|"); ok {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}
	return "", strings.TrimSpace(v)
}

// SetList replaces and persists the configured pages, clamps the index, and
// notifies. It does not navigate — changing the list shouldn't move the display.
func (p *Pages) SetList(list []Page) error {
	if err := savePages(list); err != nil {
		return err
	}
	p.mu.Lock()
	p.list = list
	if p.index >= len(list) {
		p.index = 0
	}
	obs := p.observer
	p.mu.Unlock()
	if obs != nil {
		obs()
	}
	return nil
}

// step moves the index by delta (wrapping), navigates to that page, and notifies.
func (p *Pages) step(delta int) {
	p.mu.Lock()
	n := len(p.list)
	if n == 0 {
		p.mu.Unlock()
		return
	}
	p.index = ((p.index+delta)%n + n) % n
	url := p.list[p.index].URL
	obs := p.observer
	p.mu.Unlock()
	p.navigate(url)
	if obs != nil {
		obs()
	}
}

// Next / Prev cycle forward / backward through the list.
func (p *Pages) Next() { p.step(1) }
func (p *Pages) Prev() { p.step(-1) }

// Select jumps to the page with the given label (from the HA select entity).
func (p *Pages) Select(label string) {
	p.mu.Lock()
	idx := -1
	for i, pg := range p.list {
		if pg.Label() == label {
			idx = i
			break
		}
	}
	if idx < 0 {
		p.mu.Unlock()
		return
	}
	p.index = idx
	url := p.list[idx].URL
	obs := p.observer
	p.mu.Unlock()
	p.navigate(url)
	if obs != nil {
		obs()
	}
}

// navigate hands the URL to the in-session nav agent over the FIFO. Non-blocking:
// if the kiosk session isn't up there's no reader, and we report rather than hang.
func (p *Pages) navigate(url string) error {
	f, err := os.OpenFile(p.fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("nav session not ready: %w", err)
	}
	_, werr := f.WriteString(url + "\n")
	f.Close()
	return werr
}

func loadPages() ([]Page, error) {
	b, err := os.ReadFile(urlsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var list []Page
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// savePages atomically persists the list. Mode 0664: readable by the shared
// `dashboard` group, like runtime.env (URLs aren't secret).
func savePages(list []Page) error {
	if list == nil {
		list = []Page{}
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := urlsFile + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o664); err != nil {
		return err
	}
	return os.Rename(tmp, urlsFile)
}
