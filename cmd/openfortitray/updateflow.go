package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/go-gl/glfw/v3.4/glfw"

	"github.com/savvaskoualis/openfortitray/internal/update"
	"github.com/savvaskoualis/openfortitray/internal/xopen"
)

// updateFlow is the update conversation, in one window with three states:
// offered → preparing → ready to restart.
//
// It exists because the old flow had exactly one step: click Update, and the app
// vanished. Everything — the download, the install, the relaunch — happened while
// the process was dead, so there was nothing to report progress with and no way to
// tell a working update from a hung one but waiting.
//
// The split is possible because only the final swap needs the app gone. The
// download can happen while it is alive and visible, which is also the part that
// takes the time. So the app now downloads with the window open, and asks for the
// restart only once there is something ready to install.
type updateFlow struct {
	app *app
	rel *update.Release
	win fyne.Window

	// prepareTimeout bounds the download. Injectable so a test does not have to wait.
	prepareTimeout time.Duration
}

func newUpdateFlow(a *app, rel *update.Release) *updateFlow {
	glfw.WindowHint(glfw.TransparentFramebuffer, glfw.True)
	w := a.fyneApp.NewWindow("OpenFortiTray Update")
	w.SetFixedSize(true)
	w.Resize(fyne.NewSize(460, 290))
	w.CenterOnScreen()
	// Hide rather than Close on dismissal: closing destroys the window, and
	// destroying the last window makes fyne quit the app.
	w.SetCloseIntercept(w.Hide)
	return &updateFlow{app: a, rel: rel, win: w, prepareTimeout: 10 * time.Minute}
}

// start shows the window with the update on offer.
func (f *updateFlow) start() {
	f.win.SetContent(f.app.updatePromptContent(f.rel, func(apply bool) {
		if !apply {
			f.win.Hide()
			return
		}
		f.prepare()
	}))
	f.win.Show()
	attachGlass(f.win)
	f.win.RequestFocus()
}

// prepare downloads the update with the app still running, then offers the restart.
func (f *updateFlow) prepare() {
	method := update.InstallMethod()
	f.win.SetContent(f.preparingContent())

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), f.prepareTimeout)
		defer cancel()

		log.Printf("update: preparing %s via %s", f.rel.Tag, method)
		p, err := update.Prepare(ctx, method, f.app.downloaderFor(f.rel))
		fyne.Do(func() {
			if err != nil {
				log.Printf("update: prepare failed: %v", err)
				f.win.SetContent(f.failedContent(err))
				return
			}
			log.Printf("update: prepared; waiting for the user to restart")
			f.win.SetContent(f.readyContent(method, p))
		})
	}()
}

// preparingContent is the download state. The progress bar is INDETERMINATE
// because neither path reports bytes we could believe: brew prints its own
// progress to a pipe nobody is reading, and a determinate bar that sits still is a
// worse lie than an honest spinner.
func (f *updateFlow) preparingContent() fyne.CanvasObject {
	bar := widget.NewProgressBarInfinite()
	return f.frame("Downloading update",
		"Fetching OpenFortiTray "+f.rel.Tag+". The VPN stays connected — nothing is installed yet.",
		container.NewVBox(bar), nil)
}

// readyContent asks for the restart, which is the only moment the app has to go
// away. It says why, so a window disappearing for a few seconds is expected rather
// than alarming.
func (f *updateFlow) readyContent(method update.Method, p update.Prepared) fyne.CanvasObject {
	later := widget.NewButton("Later", func() { f.win.Hide() })
	restart := widget.NewButton("Restart now", func() {
		f.win.Hide()
		f.app.finishUpdate(method, p, f.rel)
	})
	restart.Importance = widget.HighImportance

	return f.frame("Ready to install",
		"OpenFortiTray "+f.rel.Tag+" is downloaded. Installing takes a few seconds, "+
			"during which the app closes and the VPN disconnects; both come back on their own.",
		nil, container.NewHBox(layout.NewSpacer(), later, restart))
}

// failedContent reports a failed download and offers the releases page, which is
// the one thing that always works.
func (f *updateFlow) failedContent(err error) fyne.CanvasObject {
	closeBtn := widget.NewButton("Close", func() { f.win.Hide() })
	open := widget.NewButton("Open releases page", func() {
		f.win.Hide()
		_ = xopen.URL(releasesPageURL)
	})
	open.Importance = widget.HighImportance

	msg := "The update could not be downloaded. Nothing has changed — the app is " +
		"still running the version it started with.\n\n" + err.Error()
	return f.frame("Update failed", msg, nil, container.NewHBox(layout.NewSpacer(), closeBtn, open))
}

// frame lays out the states identically — heading, body, an optional middle, and
// an optional action row — so the window does not jump as it moves between them.
func (f *updateFlow) frame(heading, body string, middle, actions fyne.CanvasObject) fyne.CanvasObject {
	h := canvas.NewText(heading, theme.Color(theme.ColorNameForeground))
	h.TextSize = theme.Size(theme.SizeNameSubHeadingText)
	h.TextStyle = fyne.TextStyle{Bold: true}

	b := widget.NewLabel(body)
	b.Wrapping = fyne.TextWrapWord
	b.Importance = widget.LowImportance

	items := []fyne.CanvasObject{h, b}
	if middle != nil {
		items = append(items, middle)
	}
	items = append(items, layout.NewSpacer())
	if actions != nil {
		items = append(items, actions)
	}
	return container.NewPadded(container.NewVBox(items...))
}

// downloaderFor returns the Windows download step, or nil on the Homebrew path
// where brew does its own fetching. It is a closure so internal/update needs to
// know nothing about release assets.
func (a *app) downloaderFor(rel *update.Release) func(context.Context) (string, error) {
	if update.InstallMethod() != update.MethodWindowsInstaller {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		setup, sums := windowsUpdateAssets(rel)
		if setup == nil || sums == nil {
			return "", fmt.Errorf("release %s has no Setup.exe or SHA256SUMS", rel.Tag)
		}
		log.Printf("update: downloading %s (%d bytes)", setup.Name, setup.Size)
		return updateChecker().DownloadAndVerify(ctx, *setup, *sums)
	}
}

// finishUpdate launches the detached updater and quits, which is the only part
// that requires the app to be gone.
func (a *app) finishUpdate(method update.Method, p update.Prepared, rel *update.Release) {
	log.Printf("update: applying %s via %s", rel.Tag, method)
	if err := update.Apply(method, p.InstallerPath, os.Getpid()); err != nil {
		log.Printf("update: apply failed: %v; opening releases page", err)
		_ = xopen.URL(releasesPageURL)
		return
	}
	// If the tunnel was actually up, leave a marker so the next launch resumes it
	// regardless of the autostart/login-item setting — see shouldResumeAfterUpdate.
	if a.shouldResumeAfterUpdate() {
		if err := writeResumeMarker(a.cfgDir); err != nil {
			log.Printf("update: could not write resume marker: %v", err)
		}
	}
	// The detached updater is now waiting for this process to exit — quit gracefully
	// so the tunnel is torn down before the upgrade replaces the app.
	fyne.Do(a.Quit)
}

// updatePromptContent is the OFFER state: what is installed, what is available,
// and the two answers.
//
// The two version numbers are the only thing being asked about, so they get a
// lined-up comparison in the monospace face rather than a sentence. It replaced a
// dialog.NewConfirm whose whole message was one string with an embedded newline.
func (a *app) updatePromptContent(rel *update.Release, done func(apply bool)) fyne.CanvasObject {
	heading := canvas.NewText("Update available", theme.Color(theme.ColorNameForeground))
	heading.TextSize = theme.Size(theme.SizeNameSubHeadingText)
	heading.TextStyle = fyne.TextStyle{Bold: true}

	body := widget.NewLabel("Downloading happens now, with the VPN still connected. " +
		"OpenFortiTray only restarts once the update is ready to install.")
	body.Wrapping = fyne.TextWrapWord
	body.Importance = widget.LowImportance

	versions := container.New(layout.NewFormLayout(),
		mutedLabel("Installed"), monoLabel(version),
		mutedLabel("Available"), monoLabel(rel.Tag),
	)
	bg := canvas.NewRectangle(theme.Color(theme.ColorNameHeaderBackground))
	bg.CornerRadius = theme.Size(theme.SizeNameCardRadius)
	bg.StrokeColor = theme.Color(theme.ColorNameSeparator)
	bg.StrokeWidth = theme.Size(theme.SizeNameSeparatorThickness)
	card := container.NewStack(bg, container.NewPadded(versions))

	later := widget.NewButton("Later", func() { done(false) })
	apply := widget.NewButton("Download update", func() { done(true) })
	apply.Importance = widget.HighImportance
	actions := container.NewHBox(layout.NewSpacer(), later, apply)

	return container.NewPadded(container.NewVBox(heading, body, card, actions))
}

// mutedLabel is a key label for the small two-column cards.
func mutedLabel(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Importance = widget.LowImportance
	return l
}

// monoLabel is a right-aligned monospace value, so version numbers line up
// digit-for-digit between the two rows.
func monoLabel(text string) *widget.Label {
	return widget.NewLabelWithStyle(text, fyne.TextAlignTrailing, fyne.TextStyle{Monospace: true})
}
