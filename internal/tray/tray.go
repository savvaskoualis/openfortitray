// Package tray renders the systray menu; all logic lives in App.
package tray

import (
	"strings"

	"fyne.io/systray"
	"github.com/hyperiosoftware/hyp-vpn/internal/tunnel"
	"github.com/hyperiosoftware/hyp-vpn/internal/xopen"
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
	systray.SetTooltip("Hyperio VPN")

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

func render(e tunnel.Event, status, connect, disconnect *systray.MenuItem) {
	var title string
	switch e.State {
	case tunnel.Connected:
		systray.SetIcon(iconGreen)
		title = "Connected"
		if ip := short(e.Detail); ip != "" {
			title += " — " + ip
		}
		connect.Disable()
		disconnect.Enable()
	case tunnel.Authenticating, tunnel.Connecting, tunnel.Reconnecting:
		systray.SetIcon(iconYellow)
		title = e.State.String() + "…"
		if d := short(e.Detail); d != "" {
			title = e.State.String() + " — " + d
		}
		connect.Disable()
		disconnect.Enable()
	case tunnel.Error:
		// Error is terminal for a run: no Disconnected follows it, so the menu
		// has to offer Connect again from here.
		systray.SetIcon(iconRed)
		title = "Error"
		if d := short(e.Detail); d != "" {
			title = "Error: " + d
		}
		connect.Enable()
		disconnect.Disable()
	default:
		systray.SetIcon(iconGray)
		title = "Disconnected"
		connect.Enable()
		disconnect.Disable()
	}
	status.SetTitle(title)
	systray.SetTooltip("Hyperio VPN — " + title)
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
