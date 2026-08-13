package tray

import (
	"fyne.io/fyne/v2"
	"fyne.io/systray"

	"github.com/savvaskoualis/openfortitray/internal/xopen"
)

// nativeMenu is the tray menu built directly on fyne.io/systray, holding a handle
// to every row that changes at runtime.
//
// Why bypass fyne for this: fyne's only way to update a tray menu is
// (*fyne.Menu).Refresh, which calls SetSystemTrayMenu → systray.ResetMenu(). That
// REMOVES every native menu item and re-adds them with new ids. On macOS an open
// menu is drawn from the snapshot AppKit took when tracking began, so a rebuild
// mid-open is invisible: the status row kept saying "Connecting…" until the user
// closed and reopened the menu. systray's own add_or_update_menu_item, by
// contrast, finds the existing item by id and sets its title, which AppKit does
// reflect while the menu is open — so holding the handles and updating them is
// what makes the popover live.
//
// fyne still builds the menu once at Setup (that is what starts the tray and
// installs the icon); this replaces the rows immediately afterwards and from then
// on the fyne menu is not refreshed again.
type nativeMenu struct {
	status     *systray.MenuItem
	connect    *systray.MenuItem
	disconnect *systray.MenuItem
	auto       *systray.MenuItem
	update     *systray.MenuItem
}

// buildNativeMenu replaces the tray's rows with systray items and returns the
// handles. It returns nil if the tray is not running or the platform cannot do it
// — every systray call here panics when the tray singleton is not live, so the
// recover is what makes this best-effort rather than fatal. A nil result leaves
// the caller on fyne's rebuild-the-whole-menu path, which is correct, just not
// live while the menu is open.
//
// It must run on the UI goroutine, after the tray has started (the app's
// OnStarted hook), for the same reason SetTooltip must.
func buildNativeMenu(app App, onAuto func(want bool)) (nm *nativeMenu) {
	defer func() {
		if recover() != nil {
			nm = nil
		}
	}()

	// Drop fyne's rows first so ours are not appended after them.
	systray.ResetMenu()

	title := systray.AddMenuItem("OpenFortiTray", "")
	title.Disable()
	version := systray.AddMenuItem(app.Version(), "")
	version.Disable()
	systray.AddSeparator()

	m := &nativeMenu{}
	m.status = systray.AddMenuItem("Disconnected", "")
	m.status.Disable()
	systray.AddSeparator()

	m.connect = systray.AddMenuItem("Connect", "")
	m.disconnect = systray.AddMenuItem("Disconnect", "")
	m.disconnect.Disable()
	systray.AddSeparator()

	settings := systray.AddMenuItem("Settings…", "")
	m.auto = systray.AddMenuItemCheckbox("Auto-connect at login", "", app.AutostartEnabled())
	logs := systray.AddMenuItem("View logs", "")
	m.update = systray.AddMenuItem("Check for Updates…", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "")

	// Every click arrives on its item's channel from systray's own goroutine, so
	// each action is marshalled onto the UI goroutine — they touch windows, the
	// tunnel supervisor and the menu itself.
	onClick(m.connect, app.Connect)
	onClick(m.disconnect, app.Disconnect)
	onClick(settings, app.ShowSettings)
	onClick(logs, func() { _ = xopen.File(app.LogPath()) })
	onClick(m.update, app.UpdateClicked)
	onClick(quit, app.Quit)
	// The checkbox reports the state it is moving TO, so the caller can persist it
	// and only then tick the row.
	onClick(m.auto, func() { onAuto(!m.auto.Checked()) })

	return m
}

// onClick forwards an item's clicks to fn on the UI goroutine, for the life of the
// process. The goroutine ends when systray closes the channel at shutdown.
func onClick(item *systray.MenuItem, fn func()) {
	go func() {
		for range item.ClickedCh {
			fyne.Do(fn)
		}
	}()
}

// apply renders one view onto the native rows, in place. Titles and enabled state
// are set on the EXISTING items, which is what an open menu picks up.
func (m *nativeMenu) apply(v view) {
	m.status.SetTitle(v.title)
	if v.canConnect {
		m.connect.Enable()
		m.disconnect.Disable()
		return
	}
	m.connect.Disable()
	m.disconnect.Enable()
}

// setUpdateLabel relabels the update row in place.
func (m *nativeMenu) setUpdateLabel(label string) { m.update.SetTitle(label) }

// setAutoChecked ticks or unticks the auto-connect row in place.
func (m *nativeMenu) setAutoChecked(on bool) {
	if on {
		m.auto.Check()
		return
	}
	m.auto.Uncheck()
}
