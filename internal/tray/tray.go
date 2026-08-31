// Package tray renders the menu-bar tray on Qt6 (via miqt); all logic lives in
// App.
package tray

import (
	"fmt"

	qt "github.com/mappu/miqt/qt6"

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
	// ShowSettings opens the same window on its Connection section.
	ShowSettings()
	// ShowStatus opens the app's window on its Status section. It is the only
	// surface that shows live state.
	ShowStatus()
	// Quit begins teardown (tunnel down) and then quits the app. The tray's
	// Quit item drives this rather than any toolkit-provided default quit so
	// the VPN is always torn down before the process leaves.
	Quit()
	// UpdateClicked is the update menu item's action: apply a pending update, or
	// trigger a fresh check when none is pending. Runs on the UI goroutine.
	UpdateClicked()
}

// Controller owns the tray icon and menu and applies tunnel events to them.
// Every one of its methods mutates Qt objects, so each must run on the Qt UI
// thread: Setup runs on the main goroutine before the event loop starts; the
// menu Actions run on the UI thread by construction (Qt delivers signals on the
// thread of the receiving object, which for these is the UI thread); Apply is
// invoked only from the UI-thread event pump in cmd/openfortitray.
type Controller struct {
	app App

	icon *qt.QSystemTrayIcon
	menu *qt.QMenu

	titleAction *qt.QAction
	// statusAction is the disabled status-line row; its text is set per-state by
	// Apply.
	statusAction *qt.QAction
	// actionItem is the ONE connection action. It was two rows — Connect and
	// Disconnect — of which exactly one was always greyed out, so half of that
	// pair was permanently dead weight in a menu where every row should mean
	// something. Apply relabels it and repoints its click target together, so
	// the label and what it does can never disagree.
	actionItem     *qt.QAction
	openAction     *qt.QAction
	settingsAction *qt.QAction
	autoAction     *qt.QAction
	logsAction     *qt.QAction
	updateAction   *qt.QAction
	quitAction     *qt.QAction

	// currentAction is the click target actionItem's single OnTriggered
	// registration dispatches through. QAction.OnTriggered can only usefully be
	// registered once: calling it again stacks a second handler rather than
	// replacing the first, so Setup registers `func() { c.currentAction() }`
	// exactly once and setAction reassigns this field instead of ever calling
	// OnTriggered again.
	currentAction func()

	// icons and badgedIcons hold every QIcon the controller could ever need,
	// keyed by uistate.Kind — built once in Setup from the embedded PNGs
	// (icons.go) and, for badgedIcons, the same PNGs composited with the
	// "update available" dot (badge.go). Since the controller already owns
	// every icon it could possibly set, applying a state is just picking one of
	// these by key, no byte-comparison indirection needed.
	icons       map[uistate.Kind]*qt.QIcon
	badgedIcons map[uistate.Kind]*qt.QIcon

	// updateAvailable, once set by SetUpdateAvailable, makes iconForCurrent
	// return the badged variant of whatever connection-state icon is current —
	// so the red dot rides on top of the connection-state colour. It stays set
	// for the process's life (the app updates/relaunches to clear it).
	updateAvailable bool
	// currentKind is the uistate.Kind of the view Apply last rendered. Setup
	// leaves it at its zero value, uistate.KindIdle, matching the
	// disconnected icon Setup installs.
	currentKind uistate.Kind

	// lastView is the view most recently applied. Kept so a future re-assert
	// can re-render the current state rather than the defaults.
	lastView uistate.View
}

// trayIcon is the process's one tray icon, set by Setup. SetTooltip is a
// free function (matching the pre-Qt shape callers already use), so it reaches
// this rather than a method receiver; a nil check stands in for the old
// recover()-guarded workaround now that Qt's SetToolTip is a real,
// always-available API rather than a foreign package's singleton.
var trayIcon *qt.QSystemTrayIcon

// Setup builds the tray icon and menu and shows it. It must run on the main
// goroutine, after a QApplication has already been constructed (Qt's
// QSystemTrayIcon needs no app-driver handle the way fyne's desktop.App did —
// only a live QApplication, which cmd/openfortitray guarantees by construction
// order). The error return is kept for API compatibility with the pre-Qt
// shape; Qt has no headless/mobile-driver failure mode analogous to fyne's, so
// this never actually fails today.
func Setup(app App) (*Controller, error) {
	c := &Controller{app: app}

	c.icons = make(map[uistate.Kind]*qt.QIcon, 4)
	c.badgedIcons = make(map[uistate.Kind]*qt.QIcon, 4)
	for _, k := range []uistate.Kind{uistate.KindIdle, uistate.KindBusy, uistate.KindOK, uistate.KindBad} {
		base := iconFor(k)
		c.icons[k] = iconFromPNG(base)
		c.badgedIcons[k] = iconFromPNG(badgedPNG(base))
	}

	c.buildMenu()

	c.icon = qt.NewQSystemTrayIcon()
	c.icon.SetContextMenu(c.menu)
	c.icon.SetIcon(c.iconForCurrent())
	c.icon.Show()
	trayIcon = c.icon

	return c, nil
}

// SetTooltip sets the menu-bar icon's hover tooltip. It is a no-op before the
// tray exists (before Setup has run, or on a platform where it never will).
func SetTooltip(text string) {
	if trayIcon != nil {
		trayIcon.SetToolTip(text)
	}
}

// ShowMessage posts a desktop notification via the tray icon's native
// balloon/banner (QSystemTrayIcon::showMessage). Like SetTooltip, it is a
// no-op before the tray exists — cmd/openfortitray wires this as the app's
// notify seam (app.notify), which is nil-checked by every caller, so this
// guard only matters for a call arriving in the narrow window before Setup.
func ShowMessage(title, body string) {
	if trayIcon != nil {
		trayIcon.ShowMessage2(title, body)
	}
}

// buildMenu builds the menu items and installs them on c.menu. It touches no
// QSystemTrayIcon, so it is exercised directly by tests that only care about
// the menu's wiring.
//
// GROUPING — four bands, each answering a different question:
//
//	what is this        title+version
//	what is it doing    status, and the one thing to do about it
//	where do I go        the two windows
//	everything else     preference, diagnostics, update, quit
func (c *Controller) buildMenu() {
	app := c.app
	menu := qt.NewQMenu2()

	// One disabled header carrying both identity and build.
	c.titleAction = menu.AddActionWithText(fmt.Sprintf("OpenFortiTray %s", app.Version()))
	c.titleAction.SetEnabled(false)
	menu.AddSeparator()

	// The status line. Disabled: it is a label, not a control. Its text is set
	// per-state by Apply.
	c.statusAction = menu.AddActionWithText("")
	c.statusAction.SetEnabled(false)

	// The single connection action. Its label and its click target are set
	// together by setAction; it starts as Connect because nothing is up at
	// launch. OnTriggered is registered exactly once, here — see the
	// currentAction field comment.
	c.actionItem = menu.AddActionWithText("Connect")
	c.currentAction = app.Connect
	c.actionItem.OnTriggered(func() { c.currentAction() })

	menu.AddSeparator()

	// "Open" rather than "Status…": there is one window now, and this row opens
	// it — on the Status section, which is what someone opening a VPN client
	// wants to see. It leads the actionable rows because it is the surface that
	// actually shows live state.
	c.openAction = menu.AddActionWithText("Open")
	c.openAction.OnTriggered(app.ShowStatus)

	c.settingsAction = menu.AddActionWithText("Settings…")
	c.settingsAction.OnTriggered(app.ShowSettings)

	menu.AddSeparator()

	c.autoAction = menu.AddActionWithText("Auto-connect at login")
	c.autoAction.SetCheckable(true)
	c.autoAction.SetChecked(app.AutostartEnabled())
	c.autoAction.OnTriggered(func() { c.toggleAutostart() })

	c.logsAction = menu.AddActionWithText("View logs")
	c.logsAction.OnTriggered(func() { _ = xopen.File(app.LogPath()) })

	// The update row. It starts as a manual "Check for Updates…"; when the
	// background checker finds a newer release, SetUpdateAvailable relabels it
	// to "Update to <version> & Restart". Its click target (UpdateClicked)
	// decides which of the two it is, so the label and behaviour stay in sync
	// via one code path — its OnTriggered target never changes.
	c.updateAction = menu.AddActionWithText("Check for Updates…")
	c.updateAction.OnTriggered(app.UpdateClicked)

	menu.AddSeparator()

	// Quit drives our own teardown rather than any toolkit default quit, so the
	// tunnel always comes down before the process leaves.
	c.quitAction = menu.AddActionWithText("Quit")
	c.quitAction.OnTriggered(app.Quit)

	c.menu = menu
}

// toggleAutostart persists the login-item state the checkbox was already
// switched to and, only on failure, switches it back.
//
// Unlike fyne's plain Checked field, a checkable QAction flips its own Checked
// state BEFORE emitting triggered() — Qt has already applied the click by the
// time this runs. So the row is read, not computed by negation: want is
// whatever the row now shows, and a failed SetAutostart is undone by flipping
// the row back rather than by leaving it alone.
func (c *Controller) toggleAutostart() {
	want := c.autoAction.IsChecked()
	if err := c.app.SetAutostart(want); err != nil {
		c.autoAction.SetChecked(!want)
	}
}

// Apply renders one tunnel event onto the tray: icon, status label, and the
// connection action's label/target. It must be called on the UI thread (the
// event pump marshals it there).
func (c *Controller) Apply(e tunnel.Event) {
	v := uistate.ViewFor(e)
	c.currentKind = v.Kind
	c.lastView = v
	c.icon.SetIcon(c.iconForCurrent())
	c.statusAction.SetText(v.MenuLabel)
	c.setAction(v)
}

// setAction points the single connection row at the thing that makes sense
// now.
//
// Connect when nothing is running; otherwise the action that stops what is —
// labelled "Cancel" while a sign-in or retry is in flight, because there is no
// connection yet to "disconnect" and calling it that would be a lie. Both
// route to Disconnect, which is what tears an attempt down.
//
// The label and the click target are assigned together and read from the same
// view. It reassigns c.currentAction rather than calling actionItem.OnTriggered
// again — see that field's comment.
func (c *Controller) setAction(v uistate.View) {
	switch {
	case v.CanConnect:
		c.actionItem.SetText("Connect")
		c.currentAction = c.app.Connect
	case v.Busy():
		c.actionItem.SetText("Cancel")
		c.currentAction = c.app.Disconnect
	default:
		c.actionItem.SetText("Disconnect")
		c.currentAction = c.app.Disconnect
	}
}

// iconFor maps a view's severity to the tray glyph's raw PNG bytes. Kept as raw
// bytes (rather than a QIcon) so this mapping is comparable in a test without a
// live tray.
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

// iconFromPNG decodes PNG bytes into a QIcon via a QPixmap. The four embedded
// icons (icons.go) and their badged variants are our own trusted assets, so a
// decode failure is not expected; LoadFromDataWithData's bool result is
// intentionally not checked for the same reason composeBadge's fallback exists
// — this is a construction-time helper for known-good bytes.
//
// Padded to square first (see padToSquare) since the base assets are 45x32
// and Qt's tray renders a QIcon at its native aspect ratio, unlike Fyne's
// tray which apparently normalized this itself.
func iconFromPNG(png []byte) *qt.QIcon {
	if squared, err := padToSquare(png); err == nil {
		png = squared
	}
	pixmap := qt.NewQPixmap()
	pixmap.LoadFromDataWithData(png)
	return qt.NewQIcon2(pixmap)
}

// badgedPNG composes the "update available" dot onto base (see badge.go's
// composeBadge) and returns the result, falling back to base unchanged if the
// compose fails — it never should, these are our own embedded PNGs.
func badgedPNG(base []byte) []byte {
	data, err := composeBadge(base)
	if err != nil {
		return base
	}
	return data
}

// iconForCurrent returns the QIcon for the controller's current state,
// badged if an update is available.
func (c *Controller) iconForCurrent() *qt.QIcon {
	if c.updateAvailable {
		return c.badgedIcons[c.currentKind]
	}
	return c.icons[c.currentKind]
}

// SetUpdateAvailable relabels the update row to offer a one-click update to
// `version` and restart, and overlays the red "update available" dot on the
// menu-bar icon. It sets updateAvailable so iconForCurrent badges every
// subsequent icon too, then re-applies the CURRENT state's icon badged. It
// must run on the UI thread (the caller marshals it there).
func (c *Controller) SetUpdateAvailable(version string) {
	c.updateAction.SetText("Update to " + version + " & Restart")
	c.updateAvailable = true
	c.icon.SetIcon(c.iconForCurrent())
}

// ReassertTray re-shows the tray icon and menu. Kept as a lifecycle hook for
// callers that re-run it at the same points fyne's version needed (Windows'
// pre-Run timing gap), but Qt's tray icon has no such gap — Setup's
// icon.Show() already made it visible — so this is now a cheap, idempotent
// Show() rather than a full teardown/rebuild.
func (c *Controller) ReassertTray() {
	if c.icon == nil {
		return
	}
	c.icon.Show()
}
