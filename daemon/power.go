package main

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// systemReboot and systemPowerOff ask logind to reboot / power off the machine.
// logind is the canonical entry point (it handles inhibitors and the clean
// shutdown transaction), and it carries its own polkit actions
// — org.freedesktop.login1.reboot and .power-off — which a scoped rule grants the
// dashboard-assistant user (see modules/core/daemon.nix). Both are called
// non-interactive (false): there's no seat/agent to prompt, so the polkit rule
// must allow it outright or the call is denied.
func systemReboot() error   { return logindPower("Reboot") }
func systemPowerOff() error { return logindPower("PowerOff") }

func logindPower(method string) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect system bus: %w", err)
	}
	defer conn.Close()

	login := conn.Object("org.freedesktop.login1", "/org/freedesktop/login1")
	call := login.Call("org.freedesktop.login1.Manager."+method, 0, false)
	if call.Err != nil {
		return fmt.Errorf("logind %s: %w", method, call.Err)
	}
	return nil
}
