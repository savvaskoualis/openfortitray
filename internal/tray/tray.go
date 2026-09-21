// Package tray renders the menu-bar tray on energye/systray; all logic lives
// in App.
package tray

import (
	"fmt"

	"github.com/energye/systray"
	"github.com/gen2brain/beeep"

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
//
// energye/systray's menu items (*systray.MenuItem) can only be constructed
// from inside the onReady callback passed to systray.Run — AddMenuItem
// reaches into native platform code (Cocoa/Win32/GTK) that isn't safe to
// touch before the tray's own run loop has started. So every field below
// that holds a *systray.MenuItem is nil until Setup's onReady callback has
// run, and Apply/setAction/toggleAutostart all nil-check before touching
// them, which is also what makes those methods callable directly against a
// bare &Controller{} in tests.
type Controller struct {
	app App

	// mStatus is the disabled status-line row; its text is set per-state by
	// Apply.
	mStatus *systray.MenuItem
	// mAction is the ONE connection action. It was two rows — Connect and
	// Disconnect — of which exactly one was always greyed out, so half of
	// that pair was permanently dead weight in a menu where every row should
	// mean something. setAction relabels it and repoints its click target
	// together, so the label and what it does can never disagree.
	//
	// Unlike Qt's QAction.OnTriggered (which can only usefully be registered
	// once, so the old Qt implementation dispatched through a reassignable
	// currentAction field), *systray.MenuItem.Click(fn) simply overwrites the
	// item's stored click handler each time it's called (confirmed by
	// reading github.com/energye/systray@v1.0.3/systray.go: `func (item
	// *MenuItem) Click(fn func()) { item.click = fn }`, and the click is
	// dispatched via a single `if item.click != nil { item.click() }`) — so
	// setAction can safely call mAction.Click(...) again on every event
	// without stacking handlers.
	mAction *systray.MenuItem
	// mAuto is the "Auto-connect at login" checkbox.
	mAuto *systray.MenuItem
	// mUpdate is the update row. It starts as "Check for Updates…"; its
	// click target (UpdateClicked) never changes — only SetUpdateAvailable
	// relabels it.
	mUpdate *systray.MenuItem

	// icons and badgedIcons hold every icon's raw (padded) PNG bytes the
	// controller could ever need, keyed by uistate.Kind — built once in
	// Setup from the embedded PNGs (icons.go) and, for badgedIcons, the same
	// PNGs composited with the "update available" dot (badge.go). Since the
	// controller already owns every icon it could possibly set, applying a
	// state is just picking one of these by key, no re-encoding needed.
	//
	// Both are nil on a bare &Controller{} (i.e. before Setup runs), which
	// iconForCurrent and Apply treat as "no icon available yet" rather than
	// panicking — see their comments.
	icons       map[uistate.Kind][]byte
	badgedIcons map[uistate.Kind][]byte

	// updateAvailable, once set by SetUpdateAvailable, makes iconForCurrent
	// return the badged variant of whatever connection-state icon is current
	// — so the red dot rides on top of the connection-state colour. It stays
	// set for the process's life (the app updates/relaunches to clear it).
	updateAvailable bool
	// currentKind is the uistate.Kind of the view Apply last rendered. Its
	// zero value, uistate.KindIdle, matches the disconnected icon Setup
	// installs.
	currentKind uistate.Kind

	// lastView is the view most recently applied. Kept so a future re-assert
	// can re-render the current state rather than the defaults.
	lastView uistate.View

	// onIconClick, if non-nil, is called whenever the tray icon itself
	// (rather than a menu item) is clicked. It is supplied by the caller
	// (cmd/openfortitray) since internal/tray has no Wails import to
	// position/show/hide the window itself — see Setup. Unlike a menu item's
	// Click callback, this one owns the full show-or-hide decision (a
	// Tailscale-style toggle): the caller decides whether to position+reveal
	// the window or hide it, based on the window's current visibility — see
	// cmd/openfortitray's onTrayClick.
	onIconClick func()

	// start and end are systray.RunWithExternalLoop's returned closures.
	// They MUST be invoked from the host toolkit's own main-thread lifecycle
	// (Wails' OnStartup/OnShutdown, via Start/End below) rather than from a
	// spawned goroutine: energye/systray's package init() calls
	// runtime.LockOSThread() with no matching Unlock, permanently pinning
	// whichever goroutine runs it (main.main()'s own goroutine) to OS thread
	// 0. On Catalina+, nativeLoop's `[NSApp run]` (invoked by systray.Run,
	// the non-external-loop entry point) hard-traps (SIGTRAP) if it isn't
	// called from that exact thread — which a plain `go systray.Run(...)`
	// can never guarantee. RunWithExternalLoop's nativeStart, by contrast,
	// never calls `[NSApp run]` at all: it only sets the Cocoa delegate and
	// fires applicationDidFinishLaunching directly. That call itself is
	// NOT thread-marshalled, though (only some of systray's OTHER native
	// calls, e.g. SetIcon, go through performSelectorOnMainThread) — it
	// creates the NSStatusItem right there, so it still needs to actually
	// run on the real main thread, which is what Start's runOnMainThread
	// wrapper is for. Both of these were real, shipped v0.3.0 crashes — see
	// git history for the postmortem.
	start, end func()

	// ready and readyErr let Start block until onReady (spawned on its own
	// goroutine by systray.Register, independent of start's caller — see
	// Start's comment) has actually finished building the menu, matching the
	// synchronous contract Setup used to provide directly.
	ready    chan struct{}
	readyErr error
}

// Setup registers the tray icon and menu's callbacks but does not start
// anything native yet — see Start's doc comment for why. onIconClick is
// called (if non-nil) whenever the tray icon itself is clicked, in place of
// the show-then-reveal sequence a menu item like "Open" uses — it lets the
// caller own the full toggle decision (position+reveal, or hide) without
// internal/tray importing Wails to do so itself, or tracking
// window-visibility state that belongs to the caller (see
// cmd/openfortitray's onTrayClick/windowVisible). A nil onIconClick (e.g. in
// tests) means the tray icon click does nothing.
func Setup(app App, onIconClick func()) (*Controller, error) {
	c := &Controller{
		app:         app,
		currentKind: uistate.KindIdle,
		onIconClick: onIconClick,
		ready:       make(chan struct{}),
	}

	onReady := func() {
		// The recover MUST live here, inside onReady itself, rather than
		// wrapped around Start's call to c.start(). Verified by reading
		// systray.go's Register (which RunWithExternalLoop calls internally):
		// `// Run onReady on separate goroutine to avoid blocking event loop
		// go func() { <-readyCh; onReady() }()` — onReady is invoked on a
		// goroutine Register spawns itself, independent of whatever goroutine
		// called c.start(). A recover() around that call would sit on the
		// WRONG goroutine and never see a panic thrown from in here.
		defer func() {
			if r := recover(); r != nil {
				c.readyErr = fmt.Errorf("tray: panic during setup: %v", r)
			}
			close(c.ready)
		}()

		c.icons = make(map[uistate.Kind][]byte, 4)
		c.badgedIcons = make(map[uistate.Kind][]byte, 4)
		for _, k := range []uistate.Kind{uistate.KindIdle, uistate.KindBusy, uistate.KindOK, uistate.KindBad} {
			base := iconFor(k)
			c.icons[k] = padOrOriginal(base)
			c.badgedIcons[k] = padOrOriginal(badgedPNG(base))
		}

		systray.SetIcon(c.iconForCurrent())
		systray.SetTooltip("OpenFortiTray")

		c.buildMenu()

		// A click on the tray icon itself (not a menu item) toggles the
		// window — hide it if already visible, otherwise position+reveal it
		// on the Status view — unlike the "Open" menu row, which always
		// reveals. The whole decision lives in onIconClick (see Setup's doc
		// comment); internal/tray just forwards the click.
		systray.SetOnClick(func(menu systray.IMenu) {
			if c.onIconClick != nil {
				c.onIconClick()
			}
		})
	}

	c.start, c.end = systray.RunWithExternalLoop(onReady, func() {})

	return c, nil
}

// Start actually installs the tray icon. It should be called from the host
// toolkit's own startup lifecycle hook — cmd/openfortitray wires this into
// Wails' OnStartup — never from a bare `go` statement: see the
// Controller.start field comment for the crash that avoids. That alone is
// NOT sufficient on darwin, though: Wails' own OnStartup callback itself
// runs on a goroutine ITS frontend spawns (Frontend.Run.func1), not on the
// goroutine Go's runtime actually has locked to OS thread 0 — confirmed by a
// second real shipped crash, AppKit's own "NSWindow should only be
// instantiated on the main thread!" assertion, thrown from inside
// energye/systray's nativeStart. runOnMainThread (mainthread_darwin.go)
// closes that gap by dispatching the call onto the real main thread via
// libdispatch regardless of which goroutine Start runs on; see its own doc
// comment for the mechanism, and mainthread_other.go for why other
// platforms don't need it.
//
// Start blocks until onReady has finished building the menu, matching the
// synchronous guarantee Setup used to provide directly, and returns any
// panic onReady recovered from.
func (c *Controller) Start() error {
	defer func() {
		if r := recover(); r != nil {
			c.readyErr = fmt.Errorf("tray: panic during start: %v", r)
		}
	}()
	runOnMainThread(c.start)
	<-c.ready
	return c.readyErr
}

// End tears the tray icon down. It MUST be called from the same main-thread
// lifecycle the host toolkit calls Start from — cmd/openfortitray wires this
// into Wails' OnShutdown.
func (c *Controller) End() {
	c.end()
}

// ShowMessage posts a desktop notification via beeep, the cross-platform
// notification library energye/systray has no equivalent for (it exposes no
// balloon/banner API at all). Like the old Qt-backed version, failures are
// swallowed: cmd/openfortitray wires this as the app's best-effort notify seam
// (a.notify), which every caller already nil-checks — a failed notification
// is not something any caller can usefully react to.
func ShowMessage(title, body string) {
	_ = beeep.Notify(title, body, "")
}

// buildMenu builds the menu items. It must run inside systray.Run's onReady
// callback (see Setup) since AddMenuItem/AddMenuItemCheckbox reach into
// native platform code that isn't valid before the tray's run loop starts.
//
// GROUPING — four bands, each answering a different question:
//
//	what is this        title+version
//	what is it doing    status, and the one thing to do about it
//	where do I go        the two windows
//	everything else     preference, diagnostics, update, quit
func (c *Controller) buildMenu() {
	app := c.app

	// One disabled header carrying both identity and build.
	title := systray.AddMenuItem(fmt.Sprintf("OpenFortiTray %s", app.Version()), "")
	title.Disable()

	systray.AddSeparator()

	// The status line. Disabled: it is a label, not a control. Its text is
	// set per-state by Apply.
	c.mStatus = systray.AddMenuItem("", "")
	c.mStatus.Disable()

	// The single connection action. It starts as Connect because nothing is
	// up at launch; setAction relabels it and repoints its click target on
	// every Apply.
	c.mAction = systray.AddMenuItem("Connect", "")
	c.mAction.Click(app.Connect)

	systray.AddSeparator()

	// "Open" rather than "Status…": there is one window now, and this row
	// opens it — on the Status section, which is what someone opening a VPN
	// client wants to see. It leads the actionable rows because it is the
	// surface that actually shows live state.
	mOpen := systray.AddMenuItem("Open", "")
	mOpen.Click(app.ShowStatus)

	mSettings := systray.AddMenuItem("Settings…", "")
	mSettings.Click(app.ShowSettings)

	systray.AddSeparator()

	c.mAuto = systray.AddMenuItemCheckbox("Auto-connect at login", "", app.AutostartEnabled())
	c.mAuto.Click(func() { c.toggleAutostart() })

	mLogs := systray.AddMenuItem("View logs", "")
	mLogs.Click(func() { _ = xopen.File(app.LogPath()) })

	// The update row. It starts as a manual "Check for Updates…"; when the
	// background checker finds a newer release, SetUpdateAvailable relabels
	// it to "Update to <version> & Restart". Its click target (UpdateClicked)
	// decides which of the two it is, so the label and behaviour stay in
	// sync via one code path — its click target never changes.
	c.mUpdate = systray.AddMenuItem("Check for Updates…", "")
	c.mUpdate.Click(app.UpdateClicked)

	systray.AddSeparator()

	// Quit drives our own teardown rather than any toolkit default quit, so
	// the tunnel always comes down before the process leaves.
	mQuit := systray.AddMenuItem("Quit", "")
	mQuit.Click(app.Quit)
}

// autostartCheckbox is the subset of *systray.MenuItem's API that
// applyAutostartToggle needs, narrowed to an interface so the full
// click-then-persist-then-flip flow is testable with a fake in place of a
// live *systray.MenuItem (which can't be constructed outside systray.Run).
// *systray.MenuItem already satisfies this — Checked/Check/Uncheck are
// exactly its own method set, unchanged.
type autostartCheckbox interface {
	Checked() bool
	Check()
	Uncheck()
}

// toggleAutostart persists the login-item state the click just asked for and,
// only if that succeeds, flips the checkbox to match. See
// applyAutostartToggle for the (testable) decision itself.
func (c *Controller) toggleAutostart() {
	applyAutostartToggle(c.mAuto, c.app.SetAutostart)
}

// applyAutostartToggle computes the "Auto-connect at login" checkbox's
// intended state from a click, persists it, and mutates item only on
// success.
//
// Unlike Qt's checkable QAction, energye/systray never flips a checkbox's own
// Checked state on a click — verified directly against the library's source
// (github.com/energye/systray@v1.0.3): systray.go's systrayMenuItemSelected
// (the single dispatch point every platform backend funnels into —
// systray_darwin.m's menuHandler:, systray_windows.go's wndProc, and
// systray_menu_unix.go's dbus "clicked" event all call nothing else) only
// ever invokes `item.click()`; `checked` is mutated solely by the item's own
// Check()/Uncheck() methods. So item.Checked() here is still the PRE-click
// value, and the intended new state is its negation, not the field itself.
// There is nothing to revert to on failure since the checkbox is never
// touched until setAutostart has already succeeded.
func applyAutostartToggle(item autostartCheckbox, setAutostart func(bool) error) {
	want := !item.Checked()
	if err := setAutostart(want); err != nil {
		return
	}
	if want {
		item.Check()
	} else {
		item.Uncheck()
	}
}

// Apply renders one tunnel event onto the tray: icon, status label, and the
// connection action's label/target.
//
// Every systray/*MenuItem touch here is nil- or length-guarded so this method
// is also safe to call directly on a bare &Controller{app: fakeApp} (i.e.
// before Setup has ever run) — which is how it's unit-tested: currentKind and
// lastView are pure Go state updated unconditionally, while the systray calls
// that would otherwise require a live tray are skipped when there's nothing
// to apply them to.
func (c *Controller) Apply(e tunnel.Event) {
	v := uistate.ViewFor(e)
	c.currentKind = v.Kind
	c.lastView = v

	if icon := c.iconForCurrent(); len(icon) > 0 {
		systray.SetIcon(icon)
	}
	if c.mStatus != nil {
		c.mStatus.SetTitle(v.MenuLabel)
	}
	c.setAction(v)
}

// setAction points the single connection row at the thing that makes sense
// now. The label/target decision itself is pure (actionFor); this method
// just applies it to the live MenuItem, guarded for the nil case (before
// Setup has run).
func (c *Controller) setAction(v uistate.View) {
	if c.mAction == nil {
		return
	}
	label, target := actionFor(v, c.app)
	c.mAction.SetTitle(label)
	c.mAction.Click(target)
}

// actionFor decides the connection row's label and click target for a view.
//
// Connect when nothing is running; otherwise the action that stops what is —
// labelled "Cancel" while a sign-in or retry is in flight, because there is
// no connection yet to "disconnect" and calling it that would be a lie. Both
// non-Connect cases route to Disconnect, which is what tears an attempt down.
//
// Kept as a pure function of (View, App) — rather than inline in setAction —
// so the label/target decision is unit-testable without a live
// *systray.MenuItem.
func actionFor(v uistate.View, app App) (label string, target func()) {
	switch {
	case v.CanConnect:
		return "Connect", app.Connect
	case v.Busy():
		return "Cancel", app.Disconnect
	default:
		return "Disconnect", app.Disconnect
	}
}

// iconFor maps a view's severity to the tray glyph's raw PNG bytes. Kept as a
// pure function (rather than a method) so this mapping is testable without a
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

// padOrOriginal pads png to a square canvas (see padToSquare in badge.go),
// falling back to the original bytes if the decode fails — it never should,
// these are our own embedded assets.
func padOrOriginal(png []byte) []byte {
	if squared, err := padToSquare(png); err == nil {
		return squared
	}
	return png
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

// iconForCurrent returns the padded PNG bytes for the controller's current
// state, badged if an update is available, ready to hand straight to
// systray.SetIcon. It returns nil before Setup has populated icons/badgedIcons
// (a bare &Controller{}), which callers (Apply, Setup itself) treat as
// "nothing to set" rather than passing empty bytes to systray.SetIcon — which
// would panic (it indexes iconBytes[0] unconditionally).
func (c *Controller) iconForCurrent() []byte {
	if c.updateAvailable {
		return c.badgedIcons[c.currentKind]
	}
	return c.icons[c.currentKind]
}

// SetUpdateAvailable relabels the update row to offer a one-click update to
// `version` and restart, and overlays the red "update available" dot on the
// menu-bar icon. It sets updateAvailable so iconForCurrent badges every
// subsequent icon too, then re-applies the CURRENT state's icon badged.
func (c *Controller) SetUpdateAvailable(version string) {
	if c.mUpdate != nil {
		c.mUpdate.SetTitle("Update to " + version + " & Restart")
	}
	c.updateAvailable = true
	if icon := c.iconForCurrent(); len(icon) > 0 {
		systray.SetIcon(icon)
	}
}

// ReassertTray re-asserts the tray icon and menu at points where the pre-Qt
// fyne implementation needed to re-show it (Windows' pre-Run timing gap). Qt
// had no such gap, and neither does energye/systray at this API surface: it
// exposes no explicit "re-show" call, and SetIcon/menu construction already
// happened once inside Setup's onReady and stay live for the process's life.
// This is deliberately a no-op rather than a cop-out — there is nothing this
// library gives us to call here, and inventing a teardown/rebuild would risk
// registering duplicate menu items for no behavioural gain.
func (c *Controller) ReassertTray() {}
