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
}

// railWidth matches the approved mock.
const railWidth = 150

var navLabels = [3]string{"Status", "Connection", "Advanced"}

// New builds the window's content and returns the shell with Status
// selected. The window is left hidden; the tray reveals it.
func New(win *qt.QMainWindow, p Parts) *Shell {
	s := &Shell{win: win}

	root := qt.NewQWidget(nil)
	rootLayout := qt.NewQHBoxLayout2()
	rootLayout.SetContentsMargins(0, 0, 0, 0)
	rootLayout.SetSpacing(0)

	// Navigation is three buttons rather than a widget.List: a list of
	// three fixed destinations carries selection machinery, keyboard
	// semantics and a scrollbar for no benefit, and buttons make the
	// current section's emphasis explicit.
	rail := qt.NewQWidget(nil)
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

	rootLayout.AddWidget(rail)
	rootLayout.AddWidget(s.stack.QWidget)
	root.SetLayout(rootLayout.QLayout)

	win.SetCentralWidget(root)
	s.Select(SectionStatus)
	return s
}

// Select switches the visible content-pane section and updates the rail's
// selected-button styling.
func (s *Shell) Select(sec Section) {
	s.current = sec
	s.stack.SetCurrentIndex(int(sec))
	for i, btn := range s.navBtns {
		btn.SetChecked(Section(i) == sec)
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
