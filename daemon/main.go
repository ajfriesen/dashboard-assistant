// Command ha-dashboard-api is the management daemon for HA Dashboard OS.
//
// It owns first-boot provisioning: it computes the device state (SETUP /
// RECONNECT / READY) that the Cage/Chromium launcher polls, serves the
// on-screen setup wizard, and drives NetworkManager over D-Bus to join Wi-Fi.
package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

// State is what the kiosk launcher polls to decide which URL to display.
type State string

const (
	StateSetup     State = "SETUP"     // fresh device — show the wizard
	StateReconnect State = "RECONNECT" // provisioned but offline — show splash
	StateReady     State = "READY"     // provisioned and online — show HA
)

type server struct {
	nm   *NetworkManager // nil if no Wi-Fi device / D-Bus unavailable
	mqtt *MQTTManager    // owns the runtime-reconfigurable MQTT bridge
}

// deriveState implements the first-boot decision flow.
func (s *server) deriveState() State {
	if !Provisioned() {
		return StateSetup
	}
	if s.nm == nil || !s.nm.Connected() {
		return StateReconnect
	}
	if !ConfigValid() {
		return StateSetup
	}
	return StateReady
}

func main() {
	addr := os.Getenv("DASHBOARD_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	if err := ensureStateDir(); err != nil {
		log.Printf("warning: state dir: %v", err)
	}

	nm, err := NewNetworkManager()
	if err != nil {
		// Provisioning is degraded but the daemon still serves state/health.
		log.Printf("warning: NetworkManager unavailable: %v", err)
	}
	disp := NewDisplay()
	srv := &server{nm: nm, mqtt: NewMQTTManager(disp)}

	// MQTT bridge to Home Assistant (opt-in: disabled unless a broker is set).
	// Settings come from the environment overlaid by the runtime state file the
	// setup UI / config import write, so this also picks up later changes.
	srv.mqtt.Apply(loadMQTTConfig())

	// Reverse channel: the in-session agents report the real display power state
	// here (Off button, wake-on-touch, session restart), keeping HA in sync with
	// changes that never went through an MQTT command.
	go watchDisplayState(disp)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeText(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("/api/state", srv.handleState)

	// Provisioning surface — loopback only (the kiosk browser is local), so the
	// Wi-Fi PSK is never accepted over the LAN.
	mux.Handle("/setup", loopbackOnly(http.HandlerFunc(srv.handleSetupPage)))
	mux.Handle("/waiting", loopbackOnly(http.HandlerFunc(srv.handleWaitingPage)))
	mux.Handle("/api/netinfo", loopbackOnly(http.HandlerFunc(srv.handleNetInfo)))
	mux.Handle("/api/wifi/scan", loopbackOnly(http.HandlerFunc(srv.handleScan)))
	mux.Handle("/api/provision", loopbackOnly(http.HandlerFunc(srv.handleProvision)))
	mux.Handle("/api/reset", loopbackOnly(http.HandlerFunc(srv.handleReset)))
	// Import a YAML config bundle (HA URL / token / Wi-Fi), fed by the USB and
	// ESP importers. Loopback only, like the rest of the provisioning surface.
	mux.Handle("/api/import", loopbackOnly(http.HandlerFunc(srv.handleImport)))
	// MQTT settings surface for the setup UI: GET the current config, POST to
	// change it. Loopback only — it carries the broker password.
	mux.Handle("/api/mqtt", loopbackOnly(http.HandlerFunc(srv.handleMQTT)))

	mux.HandleFunc("/", srv.handleRoot)

	log.Printf("ha-dashboard-api listening on %s (state=%s)", addr, srv.deriveState())
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// Point a human who hits the daemon directly at the current view.
	switch s.deriveState() {
	case StateSetup:
		http.Redirect(w, r, "/setup", http.StatusFound)
	default:
		http.Redirect(w, r, "/waiting", http.StatusFound)
	}
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"state": string(s.deriveState())})
}

func (s *server) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	serveEmbedded(w, "web/setup.html")
}

func (s *server) handleWaitingPage(w http.ResponseWriter, r *http.Request) {
	serveEmbedded(w, "web/waiting.html")
}

// handleNetInfo tells the wizard whether a connection is already up (e.g. a
// wired NIC under QEMU) and whether Wi-Fi is even available, so it can offer
// "use the current connection" and skip the Wi-Fi flow.
func (s *server) handleNetInfo(w http.ResponseWriter, r *http.Request) {
	resp := struct {
		NetStatus
		HasWifi bool `json:"has_wifi"`
	}{}
	if s.nm != nil {
		resp.NetStatus = s.nm.NetInfo()
		resp.HasWifi = s.nm.HasWifi()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleScan(w http.ResponseWriter, r *http.Request) {
	if s.nm == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no wifi device"})
		return
	}
	aps, err := s.nm.Scan()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, aps)
}

type provisionRequest struct {
	SSID  string `json:"ssid"`
	PSK   string `json:"psk"`
	HAURL string `json:"ha_url"`
}

func (s *server) handleProvision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req provisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if req.HAURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ha_url required"})
		return
	}

	// Two paths:
	//   - SSID given → join that Wi-Fi network, then persist config.
	//   - SSID blank → "use the current connection" (e.g. wired): only valid if
	//     we're already on a network; skip Wi-Fi and just persist config.
	if req.SSID != "" {
		if s.nm == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no wifi device"})
			return
		}
		// Join Wi-Fi first; if it fails we stay in SETUP.
		if err := s.nm.Provision(req.SSID, req.PSK); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
	} else {
		if s.nm == nil || !s.nm.Connected() {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "not connected — choose a Wi-Fi network or attach a wired connection"})
			return
		}
	}

	if err := writeHAURL(req.HAURL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := markProvisioned(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"state": "provisioned"})

	// Restart the kiosk after the response flushes so the browser sees success.
	go func() {
		if err := restartKiosk(); err != nil {
			log.Printf("restart kiosk: %v", err)
		}
	}()
}

func (s *server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return
	}
	applied, err := s.applyImport(data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("import applied: %v", applied)
	writeJSON(w, http.StatusOK, map[string]any{"applied": applied})

	// Relaunch the kiosk so it re-reads state (new URL / provisioned).
	if len(applied) > 0 {
		go func() {
			if err := restartKiosk(); err != nil {
				log.Printf("restart kiosk: %v", err)
			}
		}()
	}
}

// mqttView is the MQTT config as exchanged with the setup UI. The password is
// write-only: GET never echoes it back (only whether one is stored, via
// PasswordSet), and a blank password on POST keeps the stored one so you can
// edit other fields without re-typing the secret.
type mqttView struct {
	Broker          string `json:"broker"`
	Username        string `json:"username"`
	Password        string `json:"password,omitempty"`
	NodeID          string `json:"node_id"`
	DiscoveryPrefix string `json:"discovery_prefix"`
	PasswordSet     bool   `json:"password_set"`
}

func (s *server) handleMQTT(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := loadMQTTConfig()
		writeJSON(w, http.StatusOK, mqttView{
			Broker:          cfg.Broker,
			Username:        cfg.Username,
			NodeID:          cfg.NodeID,
			DiscoveryPrefix: cfg.DiscoveryPrefix,
			PasswordSet:     cfg.Password != "",
		})

	case http.MethodPost:
		var in mqttView
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
			return
		}
		cfg := MQTTConfig{
			Broker:          strings.TrimSpace(in.Broker),
			Username:        strings.TrimSpace(in.Username),
			Password:        in.Password,
			NodeID:          strings.TrimSpace(in.NodeID),
			DiscoveryPrefix: strings.TrimSpace(in.DiscoveryPrefix),
		}
		// Blank password on save keeps the existing secret (the UI never receives
		// it to echo back), so editing the broker doesn't wipe the password.
		if cfg.Password == "" {
			cfg.Password = loadMQTTConfig().Password
		}
		if err := writeMQTTConfig(cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		s.mqtt.Apply(cfg.withDefaults())
		writeJSON(w, http.StatusOK, map[string]string{"state": "saved"})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

func (s *server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	if err := clearProvisioned(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": "reset"})
	go func() {
		if err := restartKiosk(); err != nil {
			log.Printf("restart kiosk: %v", err)
		}
	}()
}

// watchDisplayState tails the reverse FIFO, reporting each "on"/"off" line the
// in-session agents write into the Display (which republishes over MQTT). The
// FIFO is opened O_RDWR so the daemon always keeps a writer fd of its own —
// reads then block for data instead of hitting EOF each time a writer closes,
// and writers never get ENXIO for a missing reader. Reopens on any error.
func watchDisplayState(disp *Display) {
	for {
		f, err := os.OpenFile(displayStateFifo, os.O_RDWR, 0)
		if err != nil {
			log.Printf("display-state: open %s: %v", displayStateFifo, err)
			time.Sleep(time.Second)
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			switch {
			case line == "on":
				disp.Report(true)
			case line == "off":
				disp.Report(false)
			case strings.HasPrefix(line, "bright "):
				if n, err := strconv.Atoi(strings.TrimSpace(line[len("bright "):])); err == nil {
					disp.ReportBrightness(n)
				}
			}
		}
		if err := sc.Err(); err != nil {
			log.Printf("display-state: read %s: %v", displayStateFifo, err)
		}
		f.Close()
		time.Sleep(time.Second)
	}
}

// loopbackOnly rejects requests that did not originate from the local host.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func serveEmbedded(w http.ResponseWriter, name string) {
	b, err := webFS.ReadFile(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeText(w http.ResponseWriter, code int, s string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(s))
}
