package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"v1.5.0":  "1.5.0",
		"V2.0":    "2.0",
		"1.5.0":   "1.5.0",
		"  v3.1 ": "3.1",
		"v":       "v", // too short to be a prefix; left alone
	}
	for in, want := range cases {
		if got := normalizeVersion(in); got != want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckerFetchAndState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": "v1.5.0",
			"name": "Release 1.5.0",
			"body": "Notes",
			"html_url": "https://example/releases/v1.5.0"
		}`))
	}))
	defer srv.Close()

	u := &UpdateChecker{
		installed: "1.4.0",
		repo:      "owner/repo",
		apiBase:   srv.URL,
		interval:  time.Hour,
		client:    srv.Client(),
	}

	// Before any check, latest mirrors installed (no spurious update).
	if st := u.State(); st.Latest != "1.4.0" {
		t.Fatalf("pre-check Latest = %q, want mirror of installed", st.Latest)
	}

	var fired int
	u.SetObserver(func() { fired++ })
	u.checkOnce()

	st := u.State()
	if st.Installed != "1.4.0" || st.Latest != "1.5.0" {
		t.Fatalf("State = %+v, want installed 1.4.0 / latest 1.5.0", st)
	}
	if st.URL != "https://example/releases/v1.5.0" || st.Title != "Release 1.5.0" || st.Summary != "Notes" {
		t.Fatalf("State metadata = %+v", st)
	}
	if fired != 1 {
		t.Fatalf("observer fired %d times, want 1", fired)
	}

	// A second identical check must not re-fire the observer.
	u.checkOnce()
	if fired != 1 {
		t.Fatalf("observer re-fired on unchanged release: %d", fired)
	}
}

func TestInstallTarget(t *testing.T) {
	u := &UpdateChecker{installed: "1.4.0"}

	// No release fetched yet → nothing to install.
	if ref, ok := u.InstallTarget(); ok {
		t.Fatalf("InstallTarget before check = (%q, true), want no target", ref)
	}

	// A newer release → the raw tag is the target.
	u.latest = Release{TagName: "v1.5.0"}
	u.haveLatest = true
	if ref, ok := u.InstallTarget(); !ok || ref != "v1.5.0" {
		t.Fatalf("InstallTarget = (%q, %v), want (\"v1.5.0\", true)", ref, ok)
	}

	// Latest equals installed (after normalising the tag) → nothing to install.
	u.latest = Release{TagName: "v1.4.0"}
	if ref, ok := u.InstallTarget(); ok {
		t.Fatalf("InstallTarget when up to date = (%q, true), want no target", ref)
	}
}

func TestCheckerNoReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // GitHub/Gitea return 404 when there are no releases
	}))
	defer srv.Close()

	u := &UpdateChecker{
		installed: "1.4.0",
		repo:      "owner/repo",
		apiBase:   srv.URL,
		interval:  time.Hour,
		client:    srv.Client(),
	}
	u.SetObserver(func() { t.Fatal("observer fired on a failed check") })
	u.checkOnce()

	if st := u.State(); st.Latest != "1.4.0" {
		t.Fatalf("after 404, Latest = %q, want mirror of installed", st.Latest)
	}
}
