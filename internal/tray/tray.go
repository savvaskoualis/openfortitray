// Package tray renders the systray menu; all logic lives in App.
package tray

import (
	"strings"

	"fyne.io/systray"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/xopen"
)

// App is everything the menu needs from the application.
type App interface {
	Connect()
	Disconnect()
	SetAutostart(on bool) error
	AutostartEnabled() bool
	LogPath() string
	Events() <-chan tunnel.Event
}

// Run owns the systray lifecycle and blocks until the user quits.
func Run(app App) {
	systray.Run(func() { onReady(app) }, func() {})
}

func onReady(app App) {
	systray.SetIcon(iconGray)
	systray.SetTooltip("OpenFortiTray")

	status := systray.AddMenuItem("Disconnected", "")
	status.Disable()
	systray.AddSeparator()
	connect := systray.AddMenuItem("Connect", "")
	disconnect := systray.AddMenuItem("Disconnect", "")
	disconnect.Disable()
	systray.AddSeparator()
	auto := systray.AddMenuItemCheckbox("Auto-connect at login", "", app.AutostartEnabled())
	logs := systray.AddMenuItem("View logs", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "")

	go func() {
		for {
			select {
			case e, ok := <-app.Events():
				if !ok { // the app is going away; stop rendering
					return
				}
				render(e, status, connect, disconnect)
			case <-connect.ClickedCh:
				app.Connect()
			case <-disconnect.ClickedCh:
				app.Disconnect()
			case <-auto.ClickedCh:
				if auto.Checked() {
					if app.SetAutostart(false) == nil {
						auto.Uncheck()
					}
				} else {
					if app.SetAutostart(true) == nil {
						auto.Check()
					}
				}
			case <-logs.ClickedCh:
				_ = xopen.File(app.LogPath())
			case <-quit.ClickedCh:
				app.Disconnect()
				systray.Quit()
				return
			}
		}
	}()
}

// maxDetail caps the detail text shown in the status item; process output can
// run to many lines, and the full text is already in the log file.
const maxDetail = 60

// view is everything one event changes about the menu. It exists so the
// state→appearance mapping can be tested without a live status bar: on macOS
// systray.SetIcon goes straight into Cocoa with no no-op path for a tray that was
// never started, so a test that called render would be exercising AppKit rather
// than this package.
type view struct {
	icon  []byte
	title string
	// canConnect enables Connect and disables Disconnect; false is the reverse.
	// The two are always opposites — there is no state where both make sense.
	canConnect bool
}

func viewFor(e tunnel.Event) view {
	switch e.State {
	case tunnel.Connected:
		title := "Connected"
		if ip := short(e.Detail); ip != "" {
			title += " — " + ip
		}
		return view{icon: iconGreen, title: title}
	case tunnel.Authenticating, tunnel.Connecting, tunnel.Reconnecting:
		title := e.State.String() + "…"
		if d := short(e.Detail); d != "" {
			title = e.State.String() + " — " + d
		}
		return view{icon: iconYellow, title: title}
	case tunnel.Error:
		// Error is terminal for a run: no Disconnected follows it, so the menu
		// has to offer Connect again from here.
		title := "Error"
		if d := short(e.Detail); d != "" {
			title = "Error: " + d
		}
		return view{icon: iconRed, title: title, canConnect: true}
	default:
		return view{icon: iconGray, title: "Disconnected", canConnect: true}
	}
}

func render(e tunnel.Event, status, connect, disconnect *systray.MenuItem) {
	v := viewFor(e)
	systray.SetIcon(v.icon)
	if v.canConnect {
		connect.Enable()
		disconnect.Disable()
	} else {
		connect.Disable()
		disconnect.Enable()
	}
	status.SetTitle(v.title)
	systray.SetTooltip("OpenFortiTray — " + v.title)
}

// short reduces event detail to a single short line fit for a menu item.
func short(detail string) string {
	if i := strings.IndexAny(detail, "\r\n"); i >= 0 {
		detail = detail[:i]
	}
	detail = strings.TrimSpace(detail)
	if r := []rune(detail); len(r) > maxDetail {
		return strings.TrimSpace(string(r[:maxDetail])) + "…"
	}
	return detail
}
