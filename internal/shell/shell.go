// Package shell hosts the app's single window: a fixed nav rail on the
// left (Status/Connection/Advanced) and a QStackedWidget content pane on
// the right holding all three sections' widgets simultaneously, matching
// the already-approved "one window, one level of navigation" design.
package shell

import qt "github.com/mappu/miqt/qt6"

// Section is a destination in the window.
type Section int

const (
	// SectionStatus is the connection panel: what the tunnel is doing.
	SectionStatus Section = iota
	// SectionConnection and SectionAdvanced are the settings sections. They
	// were tabs inside the old settings window; presenting them at the same
	// level as Status keeps the whole app to ONE level of navigation, rather
	// than a window containing a rail containing tabs.
	SectionConnection
	SectionAdvanced
)

// Parts are the pieces the shell arranges, supplied by the controllers.
type Parts struct {
	Status, Connection, Advanced *qt.QWidget

	// ProfileBar, Banner and Footer belong to the settings sections only.
	ProfileBar, Banner, Footer *qt.QWidget
}

// Shell owns the window and the section switching.
type Shell struct {
	// AttachGlass, when non-nil, is called every time Reveal shows the
	// window — the app wires this to its platform glass-attach function.
	// nil is a safe no-op default so shell package tests never need real
	// native window plumbing.
	AttachGlass func(win *qt.QMainWindow)

	win     *qt.QMainWindow
	stack   *qt.QStackedWidget
	navBtns [3]*qt.QPushButton
	current Section

	// profileBar and footer are the settings-only chrome placed around the
	// stack; they are hidden on SectionStatus and shown otherwise (see
	// Select). banner is never touched by Select — it is shown/hidden only
	// by whoever owns it (settings.go's ShowIssue/hideBanner), Select just
	// guarantees it is actually placed somewhere so that visibility takes
	// effect. Any of the three may be nil (e.g. in tests that don't wire
	// settings chrome), so every use is nil-guarded.
	profileBar, banner, footer *qt.QWidget
}

// railWidth, windowWidth and windowHeight match the approved mock's scale.
const (
	railWidth    = 150
	windowWidth  = 820
	windowHeight = 680
)

var navLabels = [3]string{"Status", "Connection", "Advanced"}

// New builds the window's content and returns the shell with Status
// selected. The window is left hidden; the tray reveals it.
func New(win *qt.QMainWindow, p Parts) *Shell {
	s := &Shell{win: win}

	// Fusion (unlike the native macOS style it replaced) strictly enforces
	// WA_StyledBackground for plain QWidgets — without it, this widget's
	// QSS `background: rgba(...)` silently doesn't paint, leaving the
	// window fully see-through and looking like nothing opened at all.
	root := qt.NewQWidget(nil)
	root.SetAttribute2(qt.WA_StyledBackground, true)
	rootLayout := qt.NewQHBoxLayout2()
	rootLayout.SetContentsMargins(0, 0, 0, 0)
	rootLayout.SetSpacing(0)

	// Navigation is three buttons rather than a widget.List: a list of
	// three fixed destinations carries selection machinery, keyboard
	// semantics and a scrollbar for no benefit, and buttons make the
	// current section's emphasis explicit.
	rail := qt.NewQWidget(nil)
	rail.SetAttribute2(qt.WA_StyledBackground, true)
	rail.SetFixedWidth(railWidth)
	railLayout := qt.NewQVBoxLayout2()
	railLayout.SetContentsMargins(10, 18, 10, 18)
	railLayout.SetSpacing(4)

	group := qt.NewQButtonGroup2(rail.QObject)
	for i, label := range navLabels {
		btn := qt.NewQPushButton3(label)
		btn.SetCheckable(true)
		sec := Section(i)
		btn.OnPressed(func() { s.Select(sec) })
		group.AddButton(btn.QAbstractButton)
		railLayout.AddWidget(btn.QWidget)
		s.navBtns[i] = btn
	}
	railLayout.AddStretch()
	rail.SetLayout(railLayout.QLayout)

	// One child of this stack is visible at a time. QStackedWidget keeps
	// every section's widget alive and stateful (scroll position, focus,
	// validation) across a navigation, showing only the current one —
	// simpler than the Fyne era's manual Show/Hide-every-sibling.
	s.stack = qt.NewQStackedWidget2()
	s.stack.AddWidget(p.Status)
	s.stack.AddWidget(p.Connection)
	s.stack.AddWidget(p.Advanced)

	s.profileBar = p.ProfileBar
	s.banner = p.Banner
	s.footer = p.Footer

	// The content column stacks the settings-only chrome around the
	// QStackedWidget: ProfileBar and Banner above it, Footer below. Select
	// shows/hides ProfileBar/Footer based on section; Banner's visibility is
	// never forced here — only ShowIssue/hideBanner in settings.go toggle
	// it — this layout just gives it somewhere to actually render once they
	// do.
	content := qt.NewQWidget(nil)
	content.SetAttribute2(qt.WA_StyledBackground, true)
	contentLayout := qt.NewQVBoxLayout2()
	contentLayout.SetContentsMargins(0, 0, 0, 0)
	contentLayout.SetSpacing(0)
	if s.profileBar != nil {
		contentLayout.AddWidget(s.profileBar)
	}
	if s.banner != nil {
		contentLayout.AddWidget(s.banner)
	}
	contentLayout.AddWidget(s.stack.QWidget)
	if s.footer != nil {
		contentLayout.AddWidget(s.footer)
	}
	content.SetLayout(contentLayout.QLayout)

	rootLayout.AddWidget(rail)
	rootLayout.AddWidget(content)
	root.SetLayout(rootLayout.QLayout)

	win.SetCentralWidget(root)
	// Matches the approved mock's scale. Without an explicit size, Qt sizes
	// the window to its layout's minimum size hint — noticeably smaller
	// than intended, since nothing else in this migration ever ported the
	// original design's window dimensions forward.
	win.Resize(windowWidth, windowHeight)
	s.Select(SectionStatus)
	return s
}

// Select switches the visible content-pane section and updates the rail's
// selected-button styling. ProfileBar and Footer are settings-specific chrome
// (profile picker, Save/Cancel) shown only around the Connection/Advanced
// sections — Status is self-contained and doesn't need them. Banner's
// visibility is deliberately untouched here: it is shown on-demand by
// settings.go's ShowIssue and dismissed by hideBanner/a successful Save, not
// by navigation.
func (s *Shell) Select(sec Section) {
	s.current = sec
	s.stack.SetCurrentIndex(int(sec))
	for i, btn := range s.navBtns {
		btn.SetChecked(Section(i) == sec)
	}
	showChrome := sec != SectionStatus
	if s.profileBar != nil {
		s.profileBar.SetVisible(showChrome)
	}
	if s.footer != nil {
		s.footer.SetVisible(showChrome)
	}
}

// Current reports the visible section.
func (s *Shell) Current() Section { return s.current }

// Reveal shows the window on the given section, focuses it, and
// re-attaches native vibrancy (idempotent — safe to call on every reveal,
// since Hide/Reveal is a normal cycle for this window: the tray hides it,
// Reveal brings it back).
func (s *Shell) Reveal(sec Section) {
	s.Select(sec)
	s.win.Show()
	s.win.Raise()
	s.win.ActivateWindow()
	if s.AttachGlass != nil {
		s.AttachGlass(s.win)
	}
}
