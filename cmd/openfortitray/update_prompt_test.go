package main

import (
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/savvaskoualis/openfortitray/internal/uitheme"
	"github.com/savvaskoualis/openfortitray/internal/update"
)

// buttonsIn collects every button in a widget tree, so the assertions are about
// what the user can click rather than about the container nesting.
func buttonsIn(o fyne.CanvasObject) []*widget.Button {
	switch v := o.(type) {
	case *widget.Button:
		return []*widget.Button{v}
	case *fyne.Container:
		var out []*widget.Button
		for _, child := range v.Objects {
			out = append(out, buttonsIn(child)...)
		}
		return out
	default:
		return nil
	}
}

// The OFFER asks one question, so it offers exactly two answers and only the
// affirmative one is high-importance. Both must reach the callback with the right
// verdict: a Later that reported true would download an update the user declined.
//
// The affirmative is "Download update", not "Update & Restart". The restart is a
// SEPARATE question, asked once the download has finished — the app used to quit the
// moment this was clicked, which meant the download and install happened with the
// process dead and nothing able to report progress.
func TestUpdatePromptButtons(t *testing.T) {
	test.NewApp()
	a := &app{}

	var got []bool
	content := a.updatePromptContent(&update.Release{Tag: "v0.1.34"}, func(apply bool) {
		got = append(got, apply)
	})

	btns := buttonsIn(content)
	if len(btns) != 2 {
		t.Fatalf("prompt has %d buttons, want 2", len(btns))
	}
	var later, apply *widget.Button
	for _, b := range btns {
		switch b.Text {
		case "Later":
			later = b
		case "Download update":
			apply = b
		}
	}
	if later == nil || apply == nil {
		t.Fatalf("buttons are %q and %q, want Later and Download update", btns[0].Text, btns[1].Text)
	}
	if apply.Importance != widget.HighImportance {
		t.Error("Download update must be the high-importance action")
	}
	if later.Importance == widget.HighImportance {
		t.Error("Later must not compete with Download update")
	}

	test.Tap(later)
	test.Tap(apply)
	if want := []bool{false, true}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("callback got %v, want %v", got, want)
	}
}

// The release tag must appear verbatim: the whole point of the prompt is telling
// the user which version they are about to install.
func TestUpdatePromptShowsBothVersions(t *testing.T) {
	test.NewApp()
	a := &app{}
	content := a.updatePromptContent(&update.Release{Tag: "v9.9.9"}, func(bool) {})

	var texts []string
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch v := o.(type) {
		case *widget.Label:
			texts = append(texts, v.Text)
		case *fyne.Container:
			for _, child := range v.Objects {
				walk(child)
			}
		}
	}
	walk(content)

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

type forcedVariant struct {
	fyne.Theme
	v fyne.ThemeVariant
}

func (f forcedVariant) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return f.Theme.Color(n, f.v)
}

// TestCaptureUpdatePrompt writes PNGs of the prompt so its layout can be looked
// at without waiting for a real release. Env-gated, so it is a no-op in CI:
//
//	OFT_CAPTURE_DIR=/tmp/caps go test ./cmd/openfortitray/ -run TestCaptureUpdatePrompt
func TestCaptureUpdatePrompt(t *testing.T) {
	dir := os.Getenv("OFT_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set OFT_CAPTURE_DIR to capture window PNGs")
	}
	for name, v := range map[string]fyne.ThemeVariant{"light": 1, "dark": 0} {
		fa := test.NewApp()
		fa.Settings().SetTheme(forcedVariant{Theme: uitheme.New(), v: v})

		a := &app{}
		w := test.NewWindow(a.updatePromptContent(&update.Release{Tag: "v0.1.34"}, func(bool) {}))
		w.Resize(fyne.NewSize(460, 290))

		f, err := os.Create(dir + "/update-" + name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, w.Canvas().Capture()); err != nil {
			t.Fatal(err)
		}
		f.Close()
		w.Close()
	}
}

// The RESTART is a separate question, asked only once something is downloaded. Its
// affirmative must be the high-importance one, and Later must not trigger the
// install — an update applied against a declined restart takes the VPN down without
// consent.
func TestReadyStateAsksForTheRestart(t *testing.T) {
	test.NewApp()
	a := &app{}
	f := &updateFlow{app: a, rel: &update.Release{Tag: "v0.1.36"}, win: test.NewWindow(nil)}
	defer f.win.Close()

	btns := buttonsIn(f.readyContent(update.MethodHomebrew, update.Prepared{}))
	if len(btns) != 2 {
		t.Fatalf("ready state has %d buttons, want 2", len(btns))
	}
	var later, restart *widget.Button
	for _, b := range btns {
		switch b.Text {
		case "Later":
			later = b
		case "Restart now":
			restart = b
		}
	}
	if later == nil || restart == nil {
		t.Fatalf("buttons are %q and %q, want Later and Restart now", btns[0].Text, btns[1].Text)
	}
	if restart.Importance != widget.HighImportance {
		t.Error("Restart now must be the high-importance action")
	}
	// The body has to say what a restart costs: the app closes and the VPN drops.
	// A window vanishing unannounced is what this whole flow exists to stop.
	var text string
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch v := o.(type) {
		case *widget.Label:
			text += v.Text + " "
		case *fyne.Container:
			for _, c := range v.Objects {
				walk(c)
			}
		}
	}
	walk(f.readyContent(update.MethodHomebrew, update.Prepared{}))
	// The body must name both costs. "restart" itself is on the button; what the
	// prose has to add is what the restart DOES.
	for _, want := range []string{"closes", "disconnect"} {
		if !strings.Contains(strings.ToLower(text), want) {
			t.Errorf("the ready state does not warn that the app %q: %q", want, text)
		}
	}
}

// The download state must show it is working and must NOT offer a restart: there is
// nothing installed to restart into yet.
func TestPreparingStateShowsProgressAndNoRestart(t *testing.T) {
	test.NewApp()
	f := &updateFlow{app: &app{}, rel: &update.Release{Tag: "v0.1.36"}, win: test.NewWindow(nil)}
	defer f.win.Close()

	content := f.preparingContent()
	if len(buttonsIn(content)) != 0 {
		t.Error("the download state must offer no actions; nothing is installable yet")
	}
	var found bool
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		if _, ok := o.(*widget.ProgressBarInfinite); ok {
			found = true
		}
		if c, ok := o.(*fyne.Container); ok {
			for _, ch := range c.Objects {
				walk(ch)
			}
		}
	}
	walk(content)
	if !found {
		t.Error("the download state has no progress indicator")
	}
}
