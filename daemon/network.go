package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/godbus/dbus/v5"
)

// NetworkManager D-Bus constants.
const (
	nmService    = "org.freedesktop.NetworkManager"
	nmPath       = "/org/freedesktop/NetworkManager"
	nmIface      = "org.freedesktop.NetworkManager"
	nmDevIface   = "org.freedesktop.NetworkManager.Device"
	nmWifiIface  = "org.freedesktop.NetworkManager.Device.Wireless"
	nmApIface    = "org.freedesktop.NetworkManager.AccessPoint"
	nmActiveConn = "org.freedesktop.NetworkManager.Connection.Active"

	// NMDeviceType.WIFI
	nmDeviceTypeWifi uint32 = 2
	// NMActiveConnectionState.ACTIVATED
	nmActiveStateActivated uint32 = 2
)

// AccessPoint is a scanned Wi-Fi network, deduplicated by SSID.
type AccessPoint struct {
	SSID     string `json:"ssid"`
	Strength uint8  `json:"strength"` // 0-100
	Secure   bool   `json:"secure"`
}

// NetworkManager wraps the system-bus connection and the Wi-Fi device path.
type NetworkManager struct {
	conn    *dbus.Conn
	wifiDev dbus.ObjectPath
}

// NewNetworkManager connects to the system bus and locates the first Wi-Fi device.
func NewNetworkManager() (*NetworkManager, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect system bus: %w", err)
	}
	nm := &NetworkManager{conn: conn}
	// A Wi-Fi device is optional: wired-only hosts (e.g. QEMU) still get a
	// working D-Bus layer for connection detection and config-only provisioning.
	if dev, err := nm.findWifiDevice(); err == nil {
		nm.wifiDev = dev
	}
	return nm, nil
}

// HasWifi reports whether a Wi-Fi device is present (drives whether the wizard
// shows the Wi-Fi flow at all).
func (nm *NetworkManager) HasWifi() bool { return nm.wifiDev != "" }

func (nm *NetworkManager) Close() error { return nm.conn.Close() }

func (nm *NetworkManager) findWifiDevice() (dbus.ObjectPath, error) {
	obj := nm.conn.Object(nmService, nmPath)
	var devices []dbus.ObjectPath
	if err := obj.Call(nmIface+".GetDevices", 0).Store(&devices); err != nil {
		return "", fmt.Errorf("GetDevices: %w", err)
	}
	for _, d := range devices {
		devObj := nm.conn.Object(nmService, d)
		v, err := devObj.GetProperty(nmDevIface + ".DeviceType")
		if err != nil {
			continue
		}
		if t, ok := v.Value().(uint32); ok && t == nmDeviceTypeWifi {
			return d, nil
		}
	}
	return "", fmt.Errorf("no Wi-Fi device found")
}

// NetStatus describes the currently active primary connection, if any.
type NetStatus struct {
	Connected bool   `json:"connected"`
	Type      string `json:"type"` // "ethernet", "wifi", or the raw NM type
	Name      string `json:"name"` // connection id, e.g. "Wired connection 1"
}

// NetInfo returns the active primary connection. It keys off an *activated*
// active connection rather than NM's Connectivity property, which requires a
// connectivity-check URI that is often disabled on minimal NixOS.
func (nm *NetworkManager) NetInfo() NetStatus {
	nmObj := nm.conn.Object(nmService, nmPath)

	// Prefer the primary (default-route) connection; fall back to any activated one.
	var candidates []dbus.ObjectPath
	if v, err := nmObj.GetProperty(nmIface + ".PrimaryConnection"); err == nil {
		if p, ok := v.Value().(dbus.ObjectPath); ok && p != "/" {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		if v, err := nmObj.GetProperty(nmIface + ".ActiveConnections"); err == nil {
			if ps, ok := v.Value().([]dbus.ObjectPath); ok {
				candidates = append(candidates, ps...)
			}
		}
	}

	for _, p := range candidates {
		ac := nm.conn.Object(nmService, p)
		state := uint32(0)
		if v, err := ac.GetProperty(nmActiveConn + ".State"); err == nil {
			state, _ = v.Value().(uint32)
		}
		if state != nmActiveStateActivated {
			continue
		}
		var typ, id string
		if v, err := ac.GetProperty(nmActiveConn + ".Type"); err == nil {
			typ, _ = v.Value().(string)
		}
		if v, err := ac.GetProperty(nmActiveConn + ".Id"); err == nil {
			id, _ = v.Value().(string)
		}
		return NetStatus{Connected: true, Type: friendlyType(typ), Name: id}
	}
	return NetStatus{Connected: false}
}

func friendlyType(nmType string) string {
	switch nmType {
	case "802-3-ethernet":
		return "ethernet"
	case "802-11-wireless":
		return "wifi"
	default:
		return nmType
	}
}

// Connected is the live gate between RECONNECT and READY.
func (nm *NetworkManager) Connected() bool { return nm.NetInfo().Connected }

// Scan triggers a rescan and returns visible access points, strongest first,
// deduplicated by SSID.
func (nm *NetworkManager) Scan() ([]AccessPoint, error) {
	if nm.wifiDev == "" {
		return nil, fmt.Errorf("no Wi-Fi device")
	}
	dev := nm.conn.Object(nmService, nm.wifiDev)

	// RequestScan is best-effort; ignore "scanning too soon" style errors.
	_ = dev.Call(nmWifiIface+".RequestScan", 0, map[string]dbus.Variant{}).Err
	time.Sleep(2 * time.Second)

	var apPaths []dbus.ObjectPath
	if err := dev.Call(nmWifiIface+".GetAccessPoints", 0).Store(&apPaths); err != nil {
		return nil, fmt.Errorf("GetAccessPoints: %w", err)
	}

	best := map[string]AccessPoint{}
	for _, p := range apPaths {
		apObj := nm.conn.Object(nmService, p)
		ssidV, err := apObj.GetProperty(nmApIface + ".Ssid")
		if err != nil {
			continue
		}
		raw, ok := ssidV.Value().([]byte)
		if !ok || len(raw) == 0 {
			continue
		}
		ssid := string(raw)

		var strength uint8
		if v, err := apObj.GetProperty(nmApIface + ".Strength"); err == nil {
			strength, _ = v.Value().(uint8)
		}
		var flags, wpa, rsn uint32
		if v, err := apObj.GetProperty(nmApIface + ".Flags"); err == nil {
			flags, _ = v.Value().(uint32)
		}
		if v, err := apObj.GetProperty(nmApIface + ".WpaFlags"); err == nil {
			wpa, _ = v.Value().(uint32)
		}
		if v, err := apObj.GetProperty(nmApIface + ".RsnFlags"); err == nil {
			rsn, _ = v.Value().(uint32)
		}
		secure := flags&0x1 != 0 || wpa != 0 || rsn != 0

		if cur, ok := best[ssid]; !ok || strength > cur.Strength {
			best[ssid] = AccessPoint{SSID: ssid, Strength: strength, Secure: secure}
		}
	}

	out := make([]AccessPoint, 0, len(best))
	for _, ap := range best {
		out = append(out, ap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Strength > out[j].Strength })
	return out, nil
}

// Provision adds a persistent Wi-Fi connection and activates it immediately.
// A blank psk provisions an open network. It returns once NetworkManager has
// accepted the connection (activation continues asynchronously).
func (nm *NetworkManager) Provision(ssid, psk string) error {
	if nm.wifiDev == "" {
		return fmt.Errorf("no Wi-Fi device")
	}
	wireless := map[string]dbus.Variant{
		"ssid": dbus.MakeVariant([]byte(ssid)),
		"mode": dbus.MakeVariant("infrastructure"),
	}
	settings := map[string]map[string]dbus.Variant{
		"connection": {
			"id":          dbus.MakeVariant(ssid),
			"type":        dbus.MakeVariant("802-11-wireless"),
			"autoconnect": dbus.MakeVariant(true),
		},
		"802-11-wireless": wireless,
		"ipv4":            {"method": dbus.MakeVariant("auto")},
		"ipv6":            {"method": dbus.MakeVariant("auto")},
	}
	if psk != "" {
		settings["802-11-wireless"]["security"] = dbus.MakeVariant("802-11-wireless-security")
		settings["802-11-wireless-security"] = map[string]dbus.Variant{
			"key-mgmt": dbus.MakeVariant("wpa-psk"),
			"psk":      dbus.MakeVariant(psk),
		}
	}

	obj := nm.conn.Object(nmService, nmPath)
	var connPath, activePath dbus.ObjectPath
	err := obj.Call(nmIface+".AddAndActivateConnection", 0,
		settings, nm.wifiDev, dbus.ObjectPath("/")).Store(&connPath, &activePath)
	if err != nil {
		return fmt.Errorf("AddAndActivateConnection: %w", err)
	}
	return nil
}
