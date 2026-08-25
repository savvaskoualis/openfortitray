// Package shell is the app's single window and the navigation inside it.
//
// It exists because Status and Settings used to be two separate windows. That
// meant two things a user had to find, two things to arrange on screen, and — once
// the app grew a Dock icon — an ambiguous answer to "bring this app up": which
// window? One window with sections answers all three.
//
// The controllers it arranges own widgets, not windows. This package owns the
// window: its content, its size, what its close button does, and which section is
// on screen. Nothing else calls Show or Resize on it.
package shell

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/savvaskoualis/openfortitray/internal/status"
)

// Section is a destination in the window.
type Section int

const (
	// SectionStatus is the connection panel: what the tunnel is doing.
	SectionStatus Section = iota
	// SectionConnection and SectionAdvanced are the settings sections. They were
	// tabs inside the old settings window; presenting them at the same level as
	// Status keeps the whole app to ONE level of navigation, rather than a window
	// containing a rail containing tabs.
	SectionConnection
	SectionAdvanced
)

// Parts are the pieces the shell arranges, supplied by the controllers.
type Parts struct {
	Status     fyne.CanvasObject
	Connection fyne.CanvasObject
	Advanced   fyne.CanvasObject

	// ProfileBar, Banner and Footer belong to the settings sections only. The shell
	// hides them on Status, where a profile selector and a Save button would be
	// controls with nothing to act on.
	ProfileBar fyne.CanvasObject
	Banner     fyne.CanvasObject
	Footer     fyne.CanvasObject
}

// Window sizing. Status is the shortest section, so the window is sized for it and
// grown when a section (or the activity history) needs more: a single height that
// fits the tallest section would leave the shortest one sitting in dead space,
// which is the specific thing that made the old windows look unfinished.
const (
	width = 780
	// heightStatus is status.WindowHeight, not a second guess: the two packages
	// must agree on the Status section's base height, or toggling the activity
	// history (which resizes relative to status.WindowHeight) and the shell's own
	// resize() would disagree about where "closed" is.
	heightStatus  = status.WindowHeight
	heightSetting = 620
)

// Shell owns the window and the section switching.
type Shell struct {
	win   fyne.Window
	parts Parts

	nav     map[Section]*widget.Button
	current Section

	// stack holds all three sections; one is visible at a time.
	stack *fyne.Container

	// closeRequested is what the window's close button runs.
	closeRequested func()

	// extraHeight is a section's request for more room (the activity history), kept
	// so switching away and back does not forget it.
	extraHeight float32
}

// New builds the window's content and returns the shell with Status selected. The
// window is left hidden; the tray reveals it.
func New(win fyne.Window, p Parts) *Shell {
	s := &Shell{win: win, parts: p, nav: map[Section]*widget.Button{}}

	// Navigation is three buttons rather than a widget.List: a list of three fixed
	// destinations carries selection machinery, keyboard semantics and a scrollbar
	// for no benefit, and buttons make the current section's emphasis explicit.
	navBox := container.NewVBox()
	for _, item := range []struct {
		sec   Section
		label string
		icon  fyne.Resource
	}{
		{SectionStatus, "Status", theme.InfoIcon()},
		{SectionConnection, "Connection", theme.SettingsIcon()},
		{SectionAdvanced, "Advanced", theme.StorageIcon()},
	} {
		sec := item.sec
		b := widget.NewButtonWithIcon(item.label, item.icon, func() { s.Select(sec) })
		b.Alignment = widget.ButtonAlignLeading
		b.Importance = widget.LowImportance
		s.nav[sec] = b
		navBox.Add(b)
	}
	rail := container.NewBorder(container.NewPadded(navBox), nil, nil, nil, nil)

	// One child of this stack is visible at a time. A Stack rather than swapping
	// SetContent: the widgets keep their state (scroll position, focus, validation)
	// across a navigation, which they would not if the tree were rebuilt.
	s.stack = container.NewStack(p.Status, p.Connection, p.Advanced)

	top := container.NewVBox(p.Banner, p.ProfileBar)
	body := container.NewBorder(top, p.Footer, nil, nil, s.stack)

	content := container.NewBorder(nil, nil,
		container.NewHBox(rail, widget.NewSeparator()), nil, body)

	win.SetContent(content)
	win.SetFixedSize(false)
	// A tray app's window must never quit the process: that would take the tunnel
	// down with it. Closing hides. Held as a field as well as installed, because
	// fyne exposes no getter for a window's close intercept and a test otherwise has
	// no way to reach it.
	s.closeRequested = win.Hide
	win.SetCloseIntercept(func() { s.closeRequested() })

	s.Select(SectionStatus)
	return s
}

// Select shows one section and hides the rest, along with the settings-only
// furniture.
func (s *Shell) Select(sec Section) {
	s.current = sec
	for k, b := range s.nav {
		if k == sec {
			b.Importance = widget.MediumImportance
		} else {
			b.Importance = widget.LowImportance
		}
		b.Refresh()
	}

	show := func(o fyne.CanvasObject, visible bool) {
		if o == nil {
			return
		}
		if visible {
			o.Show()
		} else {
			o.Hide()
		}
	}
	show(s.parts.Status, sec == SectionStatus)
	show(s.parts.Connection, sec == SectionConnection)
	show(s.parts.Advanced, sec == SectionAdvanced)

	settings := sec != SectionStatus
	show(s.parts.ProfileBar, settings)
	show(s.parts.Footer, settings)
	// The banner has its own visibility (raised only by a Connect issue), so it is
	// only ever hidden here, never shown.
	if !settings {
		show(s.parts.Banner, false)
	}

	s.resize()
	s.win.Content().Refresh()
}

// Current reports the visible section.
func (s *Shell) Current() Section { return s.current }

// Reveal shows the window on the given section and focuses it. This is the one
// entry point for "bring the app up".
func (s *Shell) Reveal(sec Section) {
	s.Select(sec)
	s.win.Show()
	s.win.RequestFocus()
}

// RequestHeight lets a section ask for a taller window — the activity history uses
// it, so revealing the history opens space rather than pushing itself off the
// bottom edge. A height of 0 gives up the request.
func (s *Shell) RequestHeight(h float32) {
	s.extraHeight = h
	s.resize()
}

// resize sizes the window for the current section, honouring any outstanding
// height request.
func (s *Shell) resize() {
	h := float32(heightStatus)
	if s.current != SectionStatus {
		h = heightSetting
	}
	if s.extraHeight > h {
		h = s.extraHeight
	}
	s.win.Resize(fyne.NewSize(width, h))
}
