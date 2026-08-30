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

// newPartsWithChrome builds a Parts with ProfileBar/Banner/Footer populated,
// so tests can assert Select's show/hide behavior on them.
func newPartsWithChrome() Parts {
	return Parts{
		Status:     qt.NewQWidget(nil),
		Connection: qt.NewQWidget(nil),
		Advanced:   qt.NewQWidget(nil),
		ProfileBar: qt.NewQWidget(nil),
		Banner:     qt.NewQWidget(nil),
		Footer:     qt.NewQWidget(nil),
	}
}

// chromeResult holds the outcome of exercising Select's ProfileBar/Footer/
// Banner handling, computed in TestMain — see init() for why a real
// IsVisible() chain (which requires the window to have actually been shown)
// can't be exercised from inside a spawned Test function on macOS.
type chromeResult struct {
	// profileBar/footer visibility on Status vs. a settings section.
	statusHidesProfileBar, statusHidesFooter             bool
	sectionShowsProfileBar, sectionShowsFooter           bool
	backToStatusHidesProfileBar, backToStatusHidesFooter bool

	// banner must never be forced by Select, whichever way it was left.
	bannerStaysHiddenAcrossSelects  bool
	bannerStaysVisibleAcrossSelects bool
}

var chrome chromeResult

func TestSelectShowsSettingsChromeOnlyOffStatus(t *testing.T) {
	if !chrome.statusHidesProfileBar {
		t.Error("ProfileBar must be hidden on SectionStatus")
	}
	if !chrome.statusHidesFooter {
		t.Error("Footer must be hidden on SectionStatus")
	}
	if !chrome.sectionShowsProfileBar {
		t.Error("ProfileBar must be visible on Connection/Advanced")
	}
	if !chrome.sectionShowsFooter {
		t.Error("Footer must be visible on Connection/Advanced")
	}
	if !chrome.backToStatusHidesProfileBar {
		t.Error("ProfileBar must go back to hidden on SectionStatus")
	}
	if !chrome.backToStatusHidesFooter {
		t.Error("Footer must go back to hidden on SectionStatus")
	}
}

// TestSelectNeverTouchesBannerVisibility verifies Select does not force
// Banner's visibility either way — only its own owner (settings.go's
// ShowIssue/hideBanner) may show or hide it.
func TestSelectNeverTouchesBannerVisibility(t *testing.T) {
	if !chrome.bannerStaysHiddenAcrossSelects {
		t.Error("Select must not force Banner visible")
	}
	if !chrome.bannerStaysVisibleAcrossSelects {
		t.Error("Select must not hide a Banner someone else made visible")
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

	// Second window/shell, with the settings chrome populated, to exercise
	// Select's ProfileBar/Banner/Footer handling — needs its own real
	// Show() (see init()'s comment on why this must happen here on the
	// initial OS thread rather than inside a spawned Test function).
	chromeWin := qt.NewQMainWindow2()
	chromeParts := newPartsWithChrome()
	cs := New(chromeWin, chromeParts)
	cs.Reveal(SectionStatus)
	statusHidesProfileBar := !chromeParts.ProfileBar.IsVisible()
	statusHidesFooter := !chromeParts.Footer.IsVisible()

	cs.Select(SectionConnection)
	sectionShowsProfileBar := chromeParts.ProfileBar.IsVisible()
	sectionShowsFooter := chromeParts.Footer.IsVisible()

	cs.Select(SectionStatus)
	backToStatusHidesProfileBar := !chromeParts.ProfileBar.IsVisible()
	backToStatusHidesFooter := !chromeParts.Footer.IsVisible()

	// Banner: starts hidden (as settings.go's buildBanner leaves it).
	// Select must never force it visible, on any section.
	chromeParts.Banner.SetVisible(false)
	cs.Select(SectionConnection)
	cs.Select(SectionAdvanced)
	cs.Select(SectionStatus)
	bannerStaysHiddenAcrossSelects := !chromeParts.Banner.IsVisible()

	// If something else (settings.go) shows it, Select must not hide it
	// either, on any section.
	chromeParts.Banner.SetVisible(true)
	cs.Select(SectionConnection)
	cs.Select(SectionStatus)
	bannerStaysVisibleAcrossSelects := chromeParts.Banner.IsVisible()

	chrome = chromeResult{
		statusHidesProfileBar:           statusHidesProfileBar,
		statusHidesFooter:               statusHidesFooter,
		sectionShowsProfileBar:          sectionShowsProfileBar,
		sectionShowsFooter:              sectionShowsFooter,
		backToStatusHidesProfileBar:     backToStatusHidesProfileBar,
		backToStatusHidesFooter:         backToStatusHidesFooter,
		bannerStaysHiddenAcrossSelects:  bannerStaysHiddenAcrossSelects,
		bannerStaysVisibleAcrossSelects: bannerStaysVisibleAcrossSelects,
	}

	os.Exit(m.Run())
}
