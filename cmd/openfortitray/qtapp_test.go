package main

import (
	"os"
	"runtime"
	"testing"
)

func init() {
	// Qt's Cocoa integration on macOS requires QApplication to be constructed
	// on the process's real initial OS thread, or it aborts with "API misuse:
	// setting the main menu on a non-main thread" — but `go test` always runs
	// every Test function, top-level ones included, on a goroutine it spawns
	// fresh via t.Run -> go tRunner(...), never on the initial goroutine.
	// init() runs on the initial goroutine before any other goroutine exists,
	// so locking here keeps that goroutine — and TestMain below, which
	// testing calls directly rather than through tRunner — pinned to the real
	// main OS thread for the life of the process.
	runtime.LockOSThread()
}

// qApplicationOK records whether TestMain's construction succeeded, so the
// actual Test function (which testing runs on a different goroutine, see
// init() above) can just check the answer.
var qApplicationOK bool

// TestMain constructs the QApplication here, on the real main OS thread
// pinned by init(), rather than in a Test function — see init() for why a
// Test function is the wrong place for this call on macOS.
func TestMain(m *testing.M) {
	// The offscreen platform plugin is Qt's own documented mechanism for
	// headless test/CI environments — GitHub Actions runners have no logged-in
	// GUI session, so constructing real native windows without it risks a
	// crash during teardown (reproduced directly on two machines before this
	// was added).
	os.Setenv("QT_QPA_PLATFORM", "offscreen")
	qApplicationOK = newQApplication(os.Args) != nil
	os.Exit(m.Run())
}

// TestNewQApplicationConstructs proves the cgo+Qt6 toolchain actually links:
// if CGO_CXXFLAGS or the Qt6 pkg-config setup is broken, this package fails
// to compile/link long before this test body runs. It does not call
// execQApplication (that blocks forever without a Quit) or exercise any
// window — see the plan's Global Constraints on what's worth testing here.
func TestNewQApplicationConstructs(t *testing.T) {
	if !qApplicationOK {
		t.Fatal("newQApplication returned nil")
	}
}
