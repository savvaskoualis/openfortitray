//go:build linux

package main

import (
	"log"

	"github.com/godbus/dbus/v5"
)

// prepareForSleepMember is logind's signal, org.freedesktop.login1.Manager's
// PrepareForSleep(bool). It fires TWICE per sleep cycle: once with true right
// before the system suspends, and once with false right after it resumes. Only
// the false (post-resume) case matters here.
const prepareForSleepMember = "org.freedesktop.login1.Manager.PrepareForSleep"

// watchSystemSleep makes fn run whenever the system resumes from sleep, via
// systemd-logind's PrepareForSleep signal on the system bus. Best-effort: a
// desktop with no systemd-logind (or no D-Bus at all) only loses the automatic
// reconnect-on-wake, logged once, never fatal to the app.
func watchSystemSleep(fn func()) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		log.Printf("wake: could not connect to the system bus, sleep/wake reconnect disabled: %v", err)
		return
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.login1.Manager"),
		dbus.WithMatchMember("PrepareForSleep"),
	); err != nil {
		log.Printf("wake: could not subscribe to PrepareForSleep, sleep/wake reconnect disabled: %v", err)
		return
	}
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)
	go func() {
		for sig := range signals {
			if sig.Name != prepareForSleepMember || len(sig.Body) != 1 {
				continue
			}
			sleeping, ok := sig.Body[0].(bool)
			if !ok || sleeping {
				continue
			}
			fn()
		}
	}()
}

// watchScreenWake is a no-op on Linux — the display-sleep-without-full-
// system-sleep gap this exists to cover was diagnosed on macOS specifically;
// see wake_darwin.go's doc comment.
func watchScreenWake(fn func()) {}
