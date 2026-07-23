package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Update-checker defaults. The repo and API base are overridable from the Nix
// update module (UPDATE_REPO / UPDATE_API_BASE) so a build can track a GitHub
// mirror or a self-hosted Gitea instead — both expose the same
// <apiBase>/repos/<repo>/releases/latest shape.
const (
	defaultUpdateRepo     = "ajfriesen/dashboard-assistant"
	defaultUpdateAPIBase  = "https://api.github.com"
	defaultUpdateInterval = time.Hour
	releaseSummaryMax     = 1000 // cap the retained release-notes payload
)

// installedVersion is the release version baked into the image by the update
// module (environment.etc."dashboard-assistant/version"). Source/dirty builds have no
// file, so it reports "dev" — which never matches a release tag, i.e. "unknown".
func installedVersion() string {
	path := envOr("DASHBOARD_ASSISTANT_VERSION_FILE", "/etc/dashboard-assistant/version")
	if b, err := os.ReadFile(path); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	return "dev"
}

// Release is the subset of a GitHub/Gitea "latest release" we surface to HA.
// /releases/latest already excludes drafts and prereleases on both, so there's
// nothing to filter here.
type Release struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
}

// UpdateState is a snapshot of the update status, safe to read after State().
// Latest is normalised (leading "v" stripped) so it compares cleanly with the
// installed version.
type UpdateState struct {
	Installed  string
	Latest     string
	URL        string
	Summary    string
	Title      string
	InProgress bool
}

// UpdateChecker polls the release source for the newest version and holds the
// result for the MQTT bridge to publish. It mirrors the Display/Activity
// observer pattern: on a change it fires the observer so the bridge republishes.
type UpdateChecker struct {
	repo        string
	apiBase     string
	interval    time.Duration
	installable bool // whether HA gets an Install button (a privileged unit exists)
	client      *http.Client

	mu         sync.Mutex
	installed  string
	latest     Release
	haveLatest bool
	installing bool // an update is currently being applied

	observer func()
}

func NewUpdateChecker() *UpdateChecker {
	return &UpdateChecker{
		installed:   installedVersion(),
		repo:        envOr("UPDATE_REPO", defaultUpdateRepo),
		apiBase:     strings.TrimRight(envOr("UPDATE_API_BASE", defaultUpdateAPIBase), "/"),
		interval:    updateInterval(),
		installable: os.Getenv("UPDATE_INSTALLABLE") == "1",
		client:      &http.Client{Timeout: 15 * time.Second},
	}
}

// Installable reports whether this image can apply updates in place (a
// privileged dashboard-assistant-update@ unit is present). The bridge only offers HA an Install
// button when true.
func (u *UpdateChecker) Installable() bool { return u.installable }

// updateInterval reads UPDATE_CHECK_INTERVAL (a Go duration, e.g. "30m") or
// falls back to the hourly default.
func updateInterval() time.Duration {
	if s := os.Getenv("UPDATE_CHECK_INTERVAL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
	}
	return defaultUpdateInterval
}

// SetObserver registers a callback fired whenever the latest release changes.
func (u *UpdateChecker) SetObserver(f func()) { u.observer = f }

// Run checks immediately, then on the interval, forever. Meant to run in its own
// goroutine. A failed check keeps the previous known value and retries next tick.
func (u *UpdateChecker) Run() {
	u.checkOnce()
	for range time.Tick(u.interval) {
		u.checkOnce()
	}
}

func (u *UpdateChecker) checkOnce() {
	rel, err := u.fetchLatest()
	if err != nil {
		log.Printf("update: check %s: %v", u.repo, err)
		return
	}
	u.mu.Lock()
	changed := !u.haveLatest || u.latest.TagName != rel.TagName
	u.latest = rel
	u.haveLatest = true
	u.mu.Unlock()
	if changed && u.observer != nil {
		u.observer()
	}
}

func (u *UpdateChecker) fetchLatest() (Release, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s/repos/%s/releases/latest", u.apiBase, u.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	// GitHub rejects requests without a User-Agent; the Accept header pins the
	// v3 JSON media type (harmless to Gitea, which ignores it).
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dashboard-assistant-os")

	resp, err := u.client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Release{}, fmt.Errorf("no releases published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("http %d", resp.StatusCode)
	}
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return Release{}, err
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return Release{}, fmt.Errorf("release has no tag_name")
	}
	return rel, nil
}

// State returns the current update status. Until the first successful check
// Latest mirrors Installed, so HA shows "up to date" rather than a spurious
// update from an empty latest version.
func (u *UpdateChecker) State() UpdateState {
	u.mu.Lock()
	defer u.mu.Unlock()

	st := UpdateState{Installed: u.installed, Latest: u.installed, InProgress: u.installing}
	if u.haveLatest {
		st.Latest = normalizeVersion(u.latest.TagName)
		st.URL = u.latest.HTMLURL
		st.Title = strings.TrimSpace(u.latest.Name)
		st.Summary = summarise(u.latest.Body)
	}
	return st
}

// InstallTarget returns the raw release tag to update to (e.g. "v1.5.0"), and
// whether an update is actually available — false when no release has been
// fetched or the newest one matches the installed version.
func (u *UpdateChecker) InstallTarget() (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.haveLatest {
		return "", false
	}
	if normalizeVersion(u.latest.TagName) == u.installed {
		return "", false
	}
	return strings.TrimSpace(u.latest.TagName), true
}

// SetInstalling marks an update as in progress (or done), for the HA entity's
// in_progress flag.
func (u *UpdateChecker) SetInstalling(v bool) {
	u.mu.Lock()
	u.installing = v
	u.mu.Unlock()
}

// RefreshInstalled re-reads the baked-in version file. Called after a successful
// switch that didn't restart the daemon, so "installed" reflects the new system.
func (u *UpdateChecker) RefreshInstalled() {
	v := installedVersion()
	u.mu.Lock()
	u.installed = v
	u.mu.Unlock()
}

// normalizeVersion strips a leading "v" from a release tag ("v1.5.0" → "1.5.0")
// so tags compare cleanly against the plain installed version.
func normalizeVersion(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 1 && (s[0] == 'v' || s[0] == 'V') {
		return s[1:]
	}
	return s
}

// summarise trims the release body and caps it, keeping the retained MQTT
// payload small (HA shows it as the release notes).
func summarise(body string) string {
	body = strings.TrimSpace(body)
	if len(body) > releaseSummaryMax {
		return body[:releaseSummaryMax] + "…"
	}
	return body
}
