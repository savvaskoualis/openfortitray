// Package tray renders the menu-bar tray on fyne v2; all logic lives in App.
package tray

import (
	"bytes"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
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
	// ShowSettings reveals (and focuses) the settings window. The window is built
	// once at startup and hidden; this only shows the existing one.
	ShowSettings()
	// Quit begins teardown (tunnel down) and then quits the fyne app. The tray's
	// Quit item drives this rather than fyne's built-in quit so the VPN is always
	// torn down before the process leaves.
	Quit()
}

// Controller owns the tray menu and icon and applies tunnel events to them.
// Every one of its methods mutates fyne objects, so each must run on the fyne
// UI goroutine: Setup runs on the main goroutine before app.Run(); the menu
// Actions run on the UI goroutine by construction; Apply is invoked only from
// inside fyne.Do (see the event pump in cmd/openfortitray).
type Controller struct {
	app  App
	desk desktop.App
	menu *fyne.Menu

	statusItem     *fyne.MenuItem
	connectItem    *fyne.MenuItem
	disconnectItem *fyne.MenuItem
	autoItem       *fyne.MenuItem

	resGray, resGreen, resYellow, resRed fyne.Resource
}

// Setup builds the tray menu on the given fyne app and installs it. It must run
// on the main goroutine before app.Run(). It fails if the app is not backed by
// a desktop driver (headless/mobile), where a menu-bar tray cannot exist.
func Setup(a fyne.App, app App) (*Controller, error) {
	desk, ok := a.(desktop.App)
	if !ok {
		return nil, fmt.Errorf("tray: fyne app has no system tray (driver %T is not a desktop.App)", a.Driver())
	}
	c := newController(app)
	c.desk = desk
	desk.SetSystemTrayIcon(c.resGray)
	desk.SetSystemTrayMenu(c.menu)
	return c, nil
}

// SetTooltip sets the menu-bar icon's hover tooltip. fyne's desktop.App exposes
// no tooltip API, but it drives an internal fyne.io/systray singleton whose
// SetTooltip targets the same tray instance fyne created. This is best-effort:
// it must be called only after the tray has actually started (fyne starts it
// during app.Run, so wire it from the app's OnStarted lifecycle hook, not from
// Setup — before Run the native status item does not yet exist and the call is
// a silent no-op). The recover keeps a not-ready or unsupported platform from
// taking the app down: a missing tooltip is cosmetic, the title row is the
// guaranteed identifier.
func SetTooltip(text string) {
	defer func() { _ = recover() }()
	systray.SetTooltip(text)
}

// newController builds the menu items, menu, and icon resources. It touches no
// desktop driver, so it is exercised directly by the click-wiring test with a
// fake App and a headless test app (no display needed). Setup adds the desk.
func newController(app App) *Controller {
	c := &Controller{
		app: app,
		// The four embedded PNGs (icons.go) become fyne resources once, here,
		// rather than being re-wrapped on every icon change.
		resGray:   fyne.NewStaticResource("openfortitray_gray.png", iconGray),
		resGreen:  fyne.NewStaticResource("openfortitray_green.png", iconGreen),
		resYellow: fyne.NewStaticResource("openfortitray_yellow.png", iconYellow),
		resRed:    fyne.NewStaticResource("openfortitray_red.png", iconRed),
	}

	// A fixed, disabled header so the popover names the app. fyne's desktop tray
	// exposes no window title, and the menu-bar icon itself carries no visible
	// label, so this row (plus the best-effort SetTooltip below) is the app's
	// only in-menu identity. It never changes: no Action, always disabled.
	titleItem := fyne.NewMenuItem("OpenFortiTray", nil)
	titleItem.Disabled = true

	c.statusItem = fyne.NewMenuItem("Disconnected", nil)
	c.statusItem.Disabled = true

	c.connectItem = fyne.NewMenuItem("Connect", func() { app.Connect() })

	c.disconnectItem = fyne.NewMenuItem("Disconnect", func() { app.Disconnect() })
	c.disconnectItem.Disabled = true

	c.autoItem = fyne.NewMenuItem("Auto-connect at login", c.toggleAutostart)
	c.autoItem.Checked = app.AutostartEnabled()

	settingsItem := fyne.NewMenuItem("Settings…", func() { app.ShowSettings() })

	logsItem := fyne.NewMenuItem("View logs", func() { _ = xopen.File(app.LogPath()) })

	// Quit carries its own Action, so fyne's addMissingQuitForMenu keeps it
	// (it only injects a default d.Quit when an IsQuit item has a nil Action).
	// On the desktop driver the tray invokes item.Action() directly, so our
	// teardown runs; we do not set IsQuit ourselves and instead drive a.Quit().
	quitItem := fyne.NewMenuItem("Quit", func() { app.Quit() })

	c.menu = fyne.NewMenu("OpenFortiTray",
		titleItem,
		fyne.NewMenuItemSeparator(),
		c.statusItem,
		fyne.NewMenuItemSeparator(),
		c.connectItem,
		c.disconnectItem,
		fyne.NewMenuItemSeparator(),
		settingsItem,
		c.autoItem,
		logsItem,
		fyne.NewMenuItemSeparator(),
		quitItem,
	)
	return c
}

// toggleAutostart flips the login item and, only if that succeeds, the checkmark.
// It runs on the UI goroutine (it is a menu Action), so it mutates the item and
// refreshes the menu directly. SetAutostart already logs and rolls the OS state
// back on failure, so a failure here simply leaves the checkbox unchanged.
func (c *Controller) toggleAutostart() {
	want := !c.autoItem.Checked
	if err := c.app.SetAutostart(want); err != nil {
		return
	}
	c.autoItem.Checked = want
	c.menu.Refresh()
}

// Apply renders one tunnel event onto the tray: icon, status label, and the
// Connect/Disconnect enabled state, then refreshes the menu. It must be called
// on the UI goroutine (the event pump marshals it through fyne.Do); fyne has no
// per-item setter, so the supported route is field mutation + (*Menu).Refresh.
func (c *Controller) Apply(e tunnel.Event) {
	v := viewFor(e)
	c.desk.SetSystemTrayIcon(c.resourceFor(v.icon))
	c.statusItem.Label = v.title
	// canConnect and its opposite are always exact opposites (see view).
	c.connectItem.Disabled = !v.canConnect
	c.disconnectItem.Disabled = v.canConnect
	c.menu.Refresh()
}

// resourceFor maps a viewFor icon (raw PNG bytes, kept that way so viewFor stays
// pure and unit-testable) to the pre-wrapped fyne resource.
func (c *Controller) resourceFor(icon []byte) fyne.Resource {
	switch {
	case bytes.Equal(icon, iconGreen):
		return c.resGreen
	case bytes.Equal(icon, iconYellow):
		return c.resYellow
	case bytes.Equal(icon, iconRed):
		return c.resRed
	default:
		return c.resGray
	}
}

// maxDetail caps the detail text shown in the status item; process output can
// run to many lines, and the full text is already in the log file.
const maxDetail = 60

// view is everything one event changes about the menu. It exists so the
// state→appearance mapping can be tested without a live status bar: on macOS
// SetSystemTrayIcon goes straight into Cocoa with no no-op path for a tray that
// was never started, so a test that called Apply would be exercising AppKit
// rather than this package.
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
