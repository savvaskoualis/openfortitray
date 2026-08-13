// Package tray renders the menu-bar tray on fyne v2; all logic lives in App.
package tray

import (
	"bytes"
	"fmt"
	"log"
	"os"
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
	// Version is the build version string, shown under the title row.
	Version() string
	// ShowSettings reveals (and focuses) the settings window. The window is built
	// once at startup and hidden; this only shows the existing one.
	ShowSettings()
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

	statusItem     *fyne.MenuItem
	connectItem    *fyne.MenuItem
	disconnectItem *fyne.MenuItem
	autoItem       *fyne.MenuItem
	updateItem     *fyne.MenuItem

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

	// native, once ReassertTray has installed it, is the menu built directly on
	// fyne.io/systray. Runtime updates go to it because they then land on the
	// EXISTING native rows, which an open menu picks up — fyne's own refresh
	// rebuilds the whole menu and is invisible until the menu is reopened (see
	// native.go). nil means that takeover is unavailable (not started yet, headless,
	// or unsupported), and everything falls back to the fyne menu.
	native *nativeMenu
	// lastView is the view most recently applied, so the native takeover can adopt
	// the current state instead of starting from the "Disconnected" defaults.
	lastView view
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

	// A fixed, disabled header so the popover names the app. fyne's desktop tray
	// exposes no window title, and the menu-bar icon itself carries no visible
	// label, so this row (plus the best-effort SetTooltip below) is the app's
	// only in-menu identity. It never changes: no Action, always disabled.
	titleItem := fyne.NewMenuItem("OpenFortiTray", nil)
	titleItem.Disabled = true

	// The build version, shown as a second disabled row directly under the title.
	// Verbatim: the string already carries the leading "v" for tagged builds, or
	// "dev" for an unstamped local build.
	versionItem := fyne.NewMenuItem(app.Version(), nil)
	versionItem.Disabled = true

	c.statusItem = fyne.NewMenuItem("Disconnected", nil)
	c.statusItem.Disabled = true

	c.connectItem = fyne.NewMenuItem("Connect", func() { app.Connect() })

	c.disconnectItem = fyne.NewMenuItem("Disconnect", func() { app.Disconnect() })
	c.disconnectItem.Disabled = true

	c.autoItem = fyne.NewMenuItem("Auto-connect at login", c.toggleAutostart)
	c.autoItem.Checked = app.AutostartEnabled()

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

	c.menu = fyne.NewMenu("OpenFortiTray",
		titleItem,
		versionItem,
		fyne.NewMenuItemSeparator(),
		c.statusItem,
		fyne.NewMenuItemSeparator(),
		c.connectItem,
		c.disconnectItem,
		fyne.NewMenuItemSeparator(),
		settingsItem,
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
	if c.native != nil {
		c.native.setAutoChecked(want)
		return
	}
	c.menu.Refresh()
}

// Apply renders one tunnel event onto the tray: icon, status label, and the
// Connect/Disconnect enabled state, then refreshes the menu. It must be called
// on the UI goroutine (the event pump marshals it through fyne.Do); fyne has no
// per-item setter, so the supported route is field mutation + (*Menu).Refresh.
func (c *Controller) Apply(e tunnel.Event) {
	v := viewFor(e)
	c.currentIcon = v.icon
	c.lastView = v
	// Guarded like ReassertTray and SetUpdateAvailable: desk is nil before Setup and
	// in the tests that exercise the menu without a desktop driver.
	if c.desk != nil {
		c.desk.SetSystemTrayIcon(c.resourceFor(v.icon))
	}
	// Keep the fyne items in step even when the native menu is live: they are the
	// fallback if the takeover is ever unavailable, and they are what the tests
	// inspect.
	c.statusItem.Label = v.title
	// canConnect and its opposite are always exact opposites (see view).
	c.connectItem.Disabled = !v.canConnect
	c.disconnectItem.Disabled = v.canConnect
	if c.native != nil {
		// In place, so a menu the user is holding open updates as the state changes
		// rather than showing whatever it showed when it opened.
		c.native.apply(v)
		return
	}
	c.menu.Refresh()
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
	if c.native != nil {
		c.native.setUpdateLabel(label)
		return
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

	// The tray is live now, which is the first moment the native rows can be built
	// (see native.go for why they are worth building). Replace fyne's rows with our
	// own and adopt whatever state has already been rendered, so taking over cannot
	// reset a connected tray to "Disconnected".
	//
	// OPT-IN, and deliberately so. The takeover is the only way to update a menu the
	// user is holding open, but it cannot be exercised without a real tray — every
	// systray call panics otherwise — so it is not covered by the tests, and a
	// takeover that silently failed would leave the menu frozen on a stale state,
	// which is worse than the staleness it fixes. Until it has been confirmed on a
	// real desktop, fyne's rebuild-the-menu path stays the default.
	if os.Getenv(liveMenuEnv) != "1" {
		log.Printf("tray: menu updates via fyne (rebuild); set %s=1 for in-place updates "+
			"that also refresh a menu held open", liveMenuEnv)
		return
	}
	if nm := buildNativeMenu(c.app, c.setAutostartFromNative); nm != nil {
		log.Print("tray: in-place menu updates active")
		c.native = nm
		if c.lastView.title != "" {
			nm.apply(c.lastView)
		}
		if c.updateAvailable {
			nm.setUpdateLabel(c.updateItem.Label)
		}
		return
	}
	log.Printf("tray: %s=1 but the in-place menu could not be built; using fyne's refresh", liveMenuEnv)
}

// liveMenuEnv opts into the in-place (systray-level) menu updates. See ReassertTray.
const liveMenuEnv = "OPENFORTITRAY_LIVE_MENU"

// setAutostartFromNative is the native checkbox's action. It routes to the same
// persist-then-tick path the fyne item uses.
func (c *Controller) setAutostartFromNative(want bool) { c.setAutostart(want) }

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
