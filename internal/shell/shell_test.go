package shell

import (
	"os"
	"runtime"
	"testing"

	qt "github.com/mappu/miqt/qt6"
)

func init() {
	// Qt's Cocoa integration on macOS requires anything that materializes
	// a real NSWindow — including QMainWindow.Show(), which
	// TestRevealSelectsAndShowsWindow needs to exercise — to run on the
	// process's real initial OS thread, or it aborts with "NSWindow
	// should only be instantiated on the main thread!". `go test` runs
	// every Test function on a goroutine it spawns fresh via
	// t.Run -> go tRunner(...), never on the initial goroutine, so the
	// Show()/AttachGlass exercise below happens in TestMain instead
	// (which testing.M.Run calls directly on this goroutine) and the
	// Test function just asserts on the captured result. init() runs on
	// the initial goroutine before any other goroutine exists, so
	// locking here keeps it pinned to the real main OS thread for the
	// life of the process. (Same pattern as
	// cmd/openfortitray/qtapp_test.go.)
	runtime.LockOSThread()
}

func TestSelectSwitchesStackedWidgetIndex(t *testing.T) {
	win := qt.NewQMainWindow2()
	p := Parts{
		Status:     qt.NewQWidget(nil),
		Connection: qt.NewQWidget(nil),
		Advanced:   qt.NewQWidget(nil),
	}
	s := New(win, p)

	s.Select(SectionConnection)
	if s.Current() != SectionConnection {
		t.Fatalf("Current() = %v, want SectionConnection", s.Current())
	}

	s.Select(SectionAdvanced)
	if s.Current() != SectionAdvanced {
		t.Fatalf("Current() = %v, want SectionAdvanced", s.Current())
	}
}

// revealResult holds the outcome of exercising Reveal, computed in
// TestMain — see init() for why that exercise can't run from inside a
// Test function on macOS.
type revealResult struct {
	selectedAdvanced, glassCalled, windowVisible bool
}

var reveal revealResult

func TestRevealSelectsAndShowsWindow(t *testing.T) {
	if !reveal.selectedAdvanced {
		t.Fatal("Reveal must select the requested section")
	}
	if !reveal.glassCalled {
		t.Fatal("Reveal must call AttachGlass")
	}
	if !reveal.windowVisible {
		t.Fatal("Reveal must show the window")
	}
}

func TestMain(m *testing.M) {
	// The offscreen platform plugin is Qt's own documented mechanism for
	// headless test/CI environments — GitHub Actions runners have no logged-in
	// GUI session, so constructing real native windows without it risks a
	// crash during teardown (reproduced directly on two machines before this
	// was added).
	os.Setenv("QT_QPA_PLATFORM", "offscreen")
	qt.NewQApplication(os.Args)

	win := qt.NewQMainWindow2()
	p := Parts{
		Status:     qt.NewQWidget(nil),
		Connection: qt.NewQWidget(nil),
		Advanced:   qt.NewQWidget(nil),
	}
	s := New(win, p)
	glassCalled := false
	s.AttachGlass = func(w *qt.QMainWindow) { glassCalled = true }
	s.Reveal(SectionAdvanced)
	reveal = revealResult{
		selectedAdvanced: s.Current() == SectionAdvanced,
		glassCalled:      glassCalled,
		windowVisible:    win.IsVisible(),
	}

	os.Exit(m.Run())
}
