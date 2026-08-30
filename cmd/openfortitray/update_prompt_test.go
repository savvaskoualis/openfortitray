package main

import (
	"os"
	"strings"
	"testing"

	qt "github.com/mappu/miqt/qt6"

	"github.com/savvaskoualis/openfortitray/internal/uidispatch"
	"github.com/savvaskoualis/openfortitray/internal/uitheme"
	"github.com/savvaskoualis/openfortitray/internal/update"
)

// buttonsIn collects every QPushButton in a widget tree, so the assertions are
// about what the user can click rather than about the layout nesting. It walks
// child widgets reachable through w's own layout tree, which is how every state's
// content in this package is built (a plain widget with a layout, never
// AddLayout'd sub-layouts without a wrapping widget).
func buttonsIn(w *qt.QWidget) []*qt.QPushButton {
	if w == nil {
		return nil
	}
	var out []*qt.QPushButton
	if w.Inherits("QPushButton") {
		out = append(out, qt.UnsafeNewQPushButton(w.UnsafePointer()))
	}
	if layout := w.Layout(); layout != nil {
		for i := 0; i < layout.Count(); i++ {
			out = append(out, buttonsIn(layout.ItemAt(i).Widget())...)
		}
	}
	return out
}

// labelsIn collects the text of every QLabel in a widget tree.
func labelsIn(w *qt.QWidget) []string {
	if w == nil {
		return nil
	}
	var out []string
	if w.Inherits("QLabel") {
		out = append(out, qt.UnsafeNewQLabel(w.UnsafePointer()).Text())
	}
	if layout := w.Layout(); layout != nil {
		for i := 0; i < layout.Count(); i++ {
			out = append(out, labelsIn(layout.ItemAt(i).Widget())...)
		}
	}
	return out
}

// hasProgressBar reports whether a QProgressBar appears anywhere in the tree.
func hasProgressBar(w *qt.QWidget) bool {
	if w == nil {
		return false
	}
	if w.Inherits("QProgressBar") {
		return true
	}
	if layout := w.Layout(); layout != nil {
		for i := 0; i < layout.Count(); i++ {
			if hasProgressBar(layout.ItemAt(i).Widget()) {
				return true
			}
		}
	}
	return false
}

// The OFFER asks one question, so it offers exactly two answers and only the
// affirmative one is the dialog's default action. Both must reach the callback
// with the right verdict: a Later that reported true would download an update the
// user declined.
//
// The affirmative is "Download update", not "Update & Restart". The restart is a
// SEPARATE question, asked once the download has finished — the app used to quit the
// moment this was clicked, which meant the download and install happened with the
// process dead and nothing able to report progress.
func TestUpdatePromptButtons(t *testing.T) {
	if !qApplicationOK {
		t.Fatal("QApplication not constructed")
	}
	a := &app{}

	var got []bool
	content := a.updatePromptContent(&update.Release{Tag: "v0.1.34"}, func(apply bool) {
		got = append(got, apply)
	})

	btns := buttonsIn(content)
	if len(btns) != 2 {
		t.Fatalf("prompt has %d buttons, want 2", len(btns))
	}
	var later, apply *qt.QPushButton
	for _, b := range btns {
		switch b.Text() {
		case "Later":
			later = b
		case "Download update":
			apply = b
		}
	}
	if later == nil || apply == nil {
		t.Fatalf("buttons are %q and %q, want Later and Download update", btns[0].Text(), btns[1].Text())
	}
	if !apply.IsDefault() {
		t.Error("Download update must be the dialog's default action")
	}
	if later.IsDefault() {
		t.Error("Later must not compete with Download update")
	}

	later.Click()
	apply.Click()
	if want := []bool{false, true}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("callback got %v, want %v", got, want)
	}
}

// The release tag must appear verbatim: the whole point of the prompt is telling
// the user which version they are about to install.
func TestUpdatePromptShowsBothVersions(t *testing.T) {
	if !qApplicationOK {
		t.Fatal("QApplication not constructed")
	}
	a := &app{}
	content := a.updatePromptContent(&update.Release{Tag: "v9.9.9"}, func(bool) {})

	texts := labelsIn(content)
	seen := map[string]bool{}
	for _, s := range texts {
		seen[s] = true
	}
	if !seen["v9.9.9"] {
		t.Errorf("labels %q do not include the available version", texts)
	}
	if !seen[version] {
		t.Errorf("labels %q do not include the installed version %q", texts, version)
	}
}

// TestCaptureUpdatePrompt writes PNGs of the prompt so its layout can be looked
// at without waiting for a real release. Env-gated, so it is a no-op in CI:
//
//	OFT_CAPTURE_DIR=/tmp/caps go test ./cmd/openfortitray/ -run TestCaptureUpdatePrompt
func TestCaptureUpdatePrompt(t *testing.T) {
	if !qApplicationOK {
		t.Fatal("QApplication not constructed")
	}
	dir := os.Getenv("OFT_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set OFT_CAPTURE_DIR to capture window PNGs")
	}
	a := &app{}
	for name, dark := range map[string]bool{"light": false, "dark": true} {
		content := a.updatePromptContent(&update.Release{Tag: "v0.1.34"}, func(bool) {})
		content.SetStyleSheet(uitheme.StyleSheet(dark))
		content.Resize(460, 290)
		content.Show()
		qt.QCoreApplication_ProcessEvents()

		pixmap := content.Grab()
		if !pixmap.Save(dir + "/update-" + name + ".png") {
			t.Fatalf("could not save %s capture", name)
		}
		content.Hide()
	}
}

// The RESTART is a separate question, asked only once something is downloaded. Its
// affirmative must be the dialog's default action, and Later must not trigger the
// install — an update applied against a declined restart takes the VPN down without
// consent.
func TestReadyStateAsksForTheRestart(t *testing.T) {
	if !qApplicationOK {
		t.Fatal("QApplication not constructed")
	}
	f := &updateFlow{
		app:           &app{},
		rel:           &update.Release{Tag: "v0.1.36"},
		dlg:           qt.NewQDialog(nil),
		dispatchQueue: uidispatch.New(),
	}

	content := f.readyContent(update.MethodHomebrew, update.Prepared{})
	btns := buttonsIn(content)
	if len(btns) != 2 {
		t.Fatalf("ready state has %d buttons, want 2", len(btns))
	}
	var later, restart *qt.QPushButton
	for _, b := range btns {
		switch b.Text() {
		case "Later":
			later = b
		case "Restart now":
			restart = b
		}
	}
	if later == nil || restart == nil {
		t.Fatalf("buttons are %q and %q, want Later and Restart now", btns[0].Text(), btns[1].Text())
	}
	if !restart.IsDefault() {
		t.Error("Restart now must be the dialog's default action")
	}
	// The body has to say what a restart costs: the app closes and the VPN drops.
	// A dialog vanishing unannounced is what this whole flow exists to stop.
	text := strings.Join(labelsIn(content), " ")
	for _, want := range []string{"closes", "disconnect"} {
		if !strings.Contains(strings.ToLower(text), want) {
			t.Errorf("the ready state does not warn that the app %q: %q", want, text)
		}
	}
}

// The download state must show it is working and must NOT offer a restart: there is
// nothing installed to restart into yet.
func TestPreparingStateShowsProgressAndNoRestart(t *testing.T) {
	if !qApplicationOK {
		t.Fatal("QApplication not constructed")
	}
	f := &updateFlow{app: &app{}, rel: &update.Release{Tag: "v0.1.36"}, dlg: qt.NewQDialog(nil)}

	content := f.preparingContent()
	if len(buttonsIn(content)) != 0 {
		t.Error("the download state must offer no actions; nothing is installable yet")
	}
	if !hasProgressBar(content) {
		t.Error("the download state has no progress indicator")
	}
}
