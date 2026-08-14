package main

import (
	"image/color"
	"image/png"
	"os"
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

// The prompt asks one question, so it offers exactly two answers and only the
// affirmative one is high-importance. Both must reach the callback with the right
// verdict: a Later that reported true would install an update the user declined.
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
		case "Update & Restart":
			apply = b
		}
	}
	if later == nil || apply == nil {
		t.Fatalf("buttons are %q and %q, want Later and Update & Restart", btns[0].Text, btns[1].Text)
	}
	if apply.Importance != widget.HighImportance {
		t.Error("Update & Restart must be the high-importance action")
	}
	if later.Importance == widget.HighImportance {
		t.Error("Later must not compete with Update & Restart")
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
