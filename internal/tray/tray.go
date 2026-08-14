// Package tray renders the menu-bar tray on fyne v2; all logic lives in App.
package tray

import (
	"bytes"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/systray"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uistate"
	"github.com/savvaskoualis/openfortitray/internal/xopen"
)

// App is everything the menu needs from the application.
type App interface {
	Connect()
	Disconnect()
	SetAutostart(on bool) error
	AutostartEnabled() bool
	LogPath() string
	// Version is the build version string, shown under the title row.
	Version() string
	// ShowSettings reveals (and focuses) the settings window. The window is built
	// once at startup and hidden; this only shows the existing one.
	ShowSettings()
	// ShowStatus reveals (and focuses) the status window, on the same
	// built-once-then-hidden contract as ShowSettings. It is the only surface that
	// shows live state: this menu cannot repaint while it is open (see the KNOWN
	// LIMITATION below), so the window is where a state change is actually visible.
	ShowStatus()
	// Quit begins teardown (tunnel down) and then quits the fyne app. The tray's
	// Quit item drives this rather than fyne's built-in quit so the VPN is always
	// torn down before the process leaves.
	Quit()
	// UpdateClicked is the update menu item's action: apply a pending update, or
	// trigger a fresh check when none is pending. Runs on the UI goroutine.
	UpdateClicked()
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

	statusItem *fyne.MenuItem
	// actionItem is the ONE connection action. It was two rows — Connect and
	// Disconnect — of which exactly one was always greyed out, so half of that pair
	// was permanently dead weight in a menu where every row should mean something.
	// Apply relabels it and repoints its action together, so the label and what it
	// does can never disagree.
	actionItem *fyne.MenuItem
	autoItem   *fyne.MenuItem
	updateItem *fyne.MenuItem

	resGray, resGreen, resYellow, resRed fyne.Resource
	// Badged ("update available") variants of the four state icons, composed once
	// at construction (see newController). resourceFor returns these instead of the
	// plain resources once updateAvailable is set.
	resGrayU, resGreenU, resYellowU, resRedU fyne.Resource

	// updateAvailable, once set by SetUpdateAvailable, makes resourceFor return the
	// badged variant of whatever state icon is current — so the red dot rides on
	// top of the connection-state colour. It stays set for the process's life (the
	// app updates/relaunches to clear it).
	updateAvailable bool
	// currentIcon is the base (unbadged) PNG of the icon Apply last rendered, so
	// SetUpdateAvailable can re-apply the CURRENT state's icon with the badge. It
	// starts as the gray/disconnected icon Setup installs.
	currentIcon []byte

	// lastView is the view most recently applied. Kept so a future re-assert can
	// re-render the current state rather than the defaults.
	lastView uistate.View
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
		// Their "update available" variants — the same icons with a red dot
		// composed on at runtime (badge.go). Built once here, like the plain ones.
		resGrayU:   badgedResource("openfortitray_gray_update.png", iconGray),
		resGreenU:  badgedResource("openfortitray_green_update.png", iconGreen),
		resYellowU: badgedResource("openfortitray_yellow_update.png", iconYellow),
		resRedU:    badgedResource("openfortitray_red_update.png", iconRed),
		// Setup installs the gray/disconnected icon first, so that is what is
		// current until Apply renders an event.
		currentIcon: iconGray,
	}

	// One disabled header carrying both identity and build. These were two rows;
	// the menu-bar icon has no visible label and fyne's tray exposes no window
	// title, so the app does need to name itself here — but it does not need two
	// rows to do it, and the version is only ever read in the same glance as the
	// name.
	titleItem := fyne.NewMenuItem("OpenFortiTray "+app.Version(), nil)
	titleItem.Disabled = true

	c.statusItem = fyne.NewMenuItem("Disconnected", nil)
	c.statusItem.Disabled = true

	// The single connection action. Its label and its action are set together by
	// Apply; it starts as Connect because nothing is up at launch.
	c.actionItem = fyne.NewMenuItem("Connect", func() { app.Connect() })

	c.autoItem = fyne.NewMenuItem("Auto-connect at login", c.toggleAutostart)
	c.autoItem.Checked = app.AutostartEnabled()

	// Status… sits at the top of the actionable rows because it is the surface that
	// actually shows live state; this menu is a snapshot from the moment it opened.
	statusWindowItem := fyne.NewMenuItem("Status…", func() { app.ShowStatus() })

	settingsItem := fyne.NewMenuItem("Settings…", func() { app.ShowSettings() })

	logsItem := fyne.NewMenuItem("View logs", func() { _ = xopen.File(app.LogPath()) })

	// The update row. It starts as a manual "Check for Updates…"; when the
	// background checker finds a newer release, SetUpdateAvailable relabels it to
	// "Update to <version> & Restart". Its Action (UpdateClicked) decides which of
	// the two it is, so the label and behaviour stay in sync via one code path.
	c.updateItem = fyne.NewMenuItem("Check for Updates…", app.UpdateClicked)

	// Quit carries its own Action, so fyne's addMissingQuitForMenu keeps it
	// (it only injects a default d.Quit when an IsQuit item has a nil Action).
	// On the desktop driver the tray invokes item.Action() directly, so our
	// teardown runs; we do not set IsQuit ourselves and instead drive a.Quit().
	quitItem := fyne.NewMenuItem("Quit", func() { app.Quit() })

	// GROUPING — four bands, each answering a different question:
	//
	//   what is this        title+version
	//   what is it doing    status, and the one thing to do about it
	//   where do I go       the two windows
	//   everything else     preference, diagnostics, update, quit
	//
	// The old menu interleaved these: the autostart checkbox sat between Settings
	// and View logs, so a preference, a window and a diagnostic shared a band and
	// none of them read as belonging where they were.
	c.menu = fyne.NewMenu("OpenFortiTray",
		titleItem,
		fyne.NewMenuItemSeparator(),
		c.statusItem,
		c.actionItem,
		fyne.NewMenuItemSeparator(),
		statusWindowItem,
		settingsItem,
		fyne.NewMenuItemSeparator(),
		c.autoItem,
		logsItem,
		c.updateItem,
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
	c.setAutostart(!c.autoItem.Checked)
}

// setAutostart persists the login-item state and, only on success, ticks the row.
// It is the one path for both menus: the fyne item is kept in step either way (it
// is the fallback and what the tests inspect), and the tick is applied natively
// when the takeover is live so an open menu shows it immediately.
func (c *Controller) setAutostart(want bool) {
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
	v := uistate.ViewFor(e)
	icon := iconFor(v.Kind)
	c.currentIcon = icon
	c.lastView = v
	// Guarded like ReassertTray and SetUpdateAvailable: desk is nil before Setup and
	// in the tests that exercise the menu without a desktop driver.
	if c.desk != nil {
		c.desk.SetSystemTrayIcon(c.resourceFor(icon))
	}
	c.statusItem.Label = v.MenuLabel
	c.setAction(v)
	c.menu.Refresh()
}

// setAction points the single connection row at the thing that makes sense now.
//
// Connect when nothing is running; otherwise the action that stops what is —
// labelled "Cancel" while a sign-in or retry is in flight, because there is no
// connection yet to "disconnect" and calling it that would be a lie. Both route to
// Disconnect, which is what tears an attempt down.
//
// The label and the Action are assigned together and read from the same view, so a
// menu that cannot repaint while open still cannot show one thing and do another.
func (c *Controller) setAction(v uistate.View) {
	switch {
	case v.CanConnect:
		c.actionItem.Label = "Connect"
		c.actionItem.Action = c.app.Connect
	case v.Busy():
		c.actionItem.Label = "Cancel"
		c.actionItem.Action = c.app.Disconnect
	default:
		c.actionItem.Label = "Disconnect"
		c.actionItem.Action = c.app.Disconnect
	}
}

// iconFor maps a view's severity to the tray glyph. The icons stay raw PNG bytes
// (rather than fyne resources) so this mapping is comparable in a test without a
// status bar: on macOS SetSystemTrayIcon goes straight into Cocoa with no no-op
// path for a tray that was never started.
func iconFor(k uistate.Kind) []byte {
	switch k {
	case uistate.KindOK:
		return iconGreen
	case uistate.KindBusy:
		return iconYellow
	case uistate.KindBad:
		return iconRed
	default:
		return iconGray
	}
}

// SetUpdateAvailable relabels the update row to offer a one-click update to
// `version` and restart, and overlays the red "update available" dot on the
// menu-bar icon. It sets updateAvailable so resourceFor badges every subsequent
// icon too, then re-applies the CURRENT state's icon badged. It must run on the
// UI goroutine (the caller marshals it through fyne.Do); like Apply it mutates
// the item and refreshes the menu. desk is nil before Setup and in the wiring
// tests, so the icon re-apply is guarded.
func (c *Controller) SetUpdateAvailable(version string) {
	label := "Update to " + version + " & Restart"
	c.updateItem.Label = label
	c.updateAvailable = true
	if c.desk != nil {
		c.desk.SetSystemTrayIcon(c.resourceFor(c.currentIcon))
	}
	c.menu.Refresh()
}

// resourceFor maps a viewFor icon (raw PNG bytes, kept that way so viewFor stays
// pure and unit-testable) to the pre-wrapped fyne resource. Once an update is
// available it returns the badged variant, so the red dot rides on top of
// whatever connection-state colour is current.
// ReassertTray re-installs the icon and menu after the native tray is live. On
// Windows fyne's systray is not ready when Setup runs (before the run loop), so
// the initial SetSystemTrayIcon there logs "tray not ready yet" and no icon
// appears — the app runs but looks like nothing happened. Calling this from the
// app's OnStarted hook (fired once the tray is up) sets them again, now that it
// takes. Harmless on macOS/Linux, where the first set already worked. Must run on
// the UI goroutine (OnStarted does).
func (c *Controller) ReassertTray() {
	if c.desk == nil {
		return
	}
	c.desk.SetSystemTrayIcon(c.resourceFor(c.currentIcon))
	c.desk.SetSystemTrayMenu(c.menu)
}

// KNOWN LIMITATION — a menu held open does not update.
//
// fyne's only route to changing a tray menu is (*fyne.Menu).Refresh, which calls
// SetSystemTrayMenu → systray.ResetMenu(): every native menu item is removed and
// re-added with new ids. macOS draws an open NSMenu from the snapshot AppKit took
// when tracking began, so the rebuild is invisible until the menu is closed and
// reopened. systray's own add_or_update_menu_item WOULD update an existing item in
// place, and AppKit does reflect that live.
//
// A hybrid was tried and reverted (0.1.32/0.1.33): fyne builds the menu, then the
// rows are rebuilt at the systray level and the handles kept. It cannot work.
// Anything that makes fyne refresh again removes those items and restores fyne's,
// so the handles then point at rows that are no longer on screen — updates go
// nowhere while the visible menu, whose refresh is being skipped, freezes. It froze
// on "Disconnected" with Disconnect greyed out, on macOS and Windows, leaving the
// menu unable to control the tunnel at all. Nor can the two be kept in step: the
// fyne refresh that would fix the visible menu is exactly what destroys the native
// rows.
//
// The two real options are to build the whole tray on fyne.io/systray and not use
// fyne's tray menu at all (fyne would then no longer start or own the tray), or to
// fix Refresh upstream in fyne so it updates items in place instead of resetting.
// Until one of those is done, the menu is correct whenever it is opened and stale
// only while held open through a state change.

func (c *Controller) resourceFor(icon []byte) fyne.Resource {
	switch {
	case bytes.Equal(icon, iconGreen):
		if c.updateAvailable {
			return c.resGreenU
		}
		return c.resGreen
	case bytes.Equal(icon, iconYellow):
		if c.updateAvailable {
			return c.resYellowU
		}
		return c.resYellow
	case bytes.Equal(icon, iconRed):
		if c.updateAvailable {
			return c.resRedU
		}
		return c.resRed
	default:
		if c.updateAvailable {
			return c.resGrayU
		}
		return c.resGray
	}
}
