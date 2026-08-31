package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"github.com/savvaskoualis/openfortitray/internal/uidispatch"
	"github.com/savvaskoualis/openfortitray/internal/update"
	"github.com/savvaskoualis/openfortitray/internal/xopen"
)

// updateFlow is the update conversation, in one dialog with three states:
// offered → preparing → ready to restart.
//
// It exists because the old flow had exactly one step: click Update, and the app
// vanished. Everything — the download, the install, the relaunch — happened while
// the process was dead, so there was nothing to report progress with and no way to
// tell a working update from a hung one but waiting.
//
// The split is possible because only the final swap needs the app gone. The
// download can happen while it is alive and visible, which is also the part that
// takes the time. So the app now downloads with the dialog open, and asks for the
// restart only once there is something ready to install.
type updateFlow struct {
	app *app
	rel *update.Release
	dlg *qt.QDialog

	// layout is the dialog's single top-level layout, installed once in
	// newUpdateFlow. Qt refuses to reassign a widget's layout once one is
	// installed (a second SetLayout call is a no-op with a runtime warning),
	// so a state transition swaps the ONE child widget inside this layout
	// (see setContent) rather than calling dlg.SetLayout again.
	layout *qt.QLayout
	// content is whichever state's widget currently sits in layout, so
	// setContent knows what to remove before installing the next one.
	content *qt.QWidget

	// dispatchQueue marshals prepare's background-goroutine result back onto
	// the Qt UI thread — the replacement for fyne.Do. Task 10 wires in the
	// app's single queue instance here.
	dispatchQueue *uidispatch.Queue

	// prepareTimeout bounds the download. Injectable so a test does not have to wait.
	prepareTimeout time.Duration
}

func newUpdateFlow(a *app, rel *update.Release, dispatchQueue *uidispatch.Queue) *updateFlow {
	dlg := qt.NewQDialog(nil)
	dlg.SetWindowTitle("OpenFortiTray Update")
	dlg.SetFixedSize(qt.NewQSize2(460, 290))

	vbox := qt.NewQVBoxLayout2()
	vbox.SetContentsMargins(0, 0, 0, 0)
	dlg.SetLayout(vbox.QLayout)

	// Hide rather than destroy on close: this dialog is reused for every future
	// update offer (see start/prepare), so destroying it here would leave f.dlg
	// dangling for the next promptUpdate call. Mirrors the old
	// SetCloseIntercept(w.Hide) — QDialog is shown non-modally via Show(), never
	// Exec(), so there is no blocking event loop for Reject/Accept to unwind.
	dlg.OnCloseEvent(func(_ func(event *qt.QCloseEvent), event *qt.QCloseEvent) {
		event.Ignore()
		dlg.Hide()
	})

	return &updateFlow{
		app:            a,
		rel:            rel,
		dlg:            dlg,
		layout:         vbox.QLayout,
		dispatchQueue:  dispatchQueue,
		prepareTimeout: 10 * time.Minute,
	}
}

// setContent swaps the dialog's single visible child for w. See the layout field
// comment for why this replaces a child widget rather than the dialog's own
// top-level layout.
func (f *updateFlow) setContent(w *qt.QWidget) {
	if f.content != nil {
		f.layout.RemoveWidget(f.content)
		f.content.DeleteLater()
	}
	f.content = w
	f.layout.AddWidget(w)
}

// start shows the dialog with the update on offer.
func (f *updateFlow) start() {
	f.setContent(f.app.updatePromptContent(f.rel, func(apply bool) {
		if !apply {
			f.dlg.Hide()
			return
		}
		f.prepare()
	}))
	f.dlg.Show()
	attachGlass(f.dlg.QWidget)
	f.dlg.Raise()
	f.dlg.ActivateWindow()
}

// prepare downloads the update with the app still running, then offers the restart.
func (f *updateFlow) prepare() {
	method := update.InstallMethod()
	f.setContent(f.preparingContent())

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), f.prepareTimeout)
		defer cancel()

		log.Printf("update: preparing %s via %s", f.rel.Tag, method)
		p, err := update.Prepare(ctx, method, f.app.downloaderFor(f.rel))
		f.dispatchQueue.Post(func() {
			if err != nil {
				log.Printf("update: prepare failed: %v", err)
				f.setContent(f.failedContent(err))
				return
			}
			log.Printf("update: prepared; waiting for the user to restart")
			f.setContent(f.readyContent(method, p))
		})
	}()
}

// preparingContent is the download state. The progress bar is INDETERMINATE
// because neither path reports bytes we could believe: brew prints its own
// progress to a pipe nobody is reading, and a determinate bar that sits still is a
// worse lie than an honest spinner. SetRange(0, 0) is Qt's documented way to get
// that behavior (the ProgressBarInfinite equivalent).
func (f *updateFlow) preparingContent() *qt.QWidget {
	bar := qt.NewQProgressBar2()
	bar.SetRange(0, 0)
	return f.frame("Downloading update",
		"Fetching OpenFortiTray "+f.rel.Tag+". The VPN stays connected — nothing is installed yet.",
		bar.QWidget, nil)
}

// readyContent asks for the restart, which is the only moment the app has to go
// away. It says why, so a dialog disappearing for a few seconds is expected rather
// than alarming.
func (f *updateFlow) readyContent(method update.Method, p update.Prepared) *qt.QWidget {
	later := qt.NewQPushButton3("Later")
	later.OnClicked(func() { f.dlg.Hide() })

	restart := qt.NewQPushButton3("Restart now")
	restart.SetDefault(true)
	restart.OnClicked(func() {
		f.dlg.Hide()
		f.app.finishUpdate(method, p, f.rel, f.dispatchQueue)
	})

	return f.frame("Ready to install",
		"OpenFortiTray "+f.rel.Tag+" is downloaded. Installing takes a few seconds, "+
			"during which the app closes and the VPN disconnects; both come back on their own.",
		nil, actionsRow(later, restart))
}

// failedContent reports a failed download and offers the releases page, which is
// the one thing that always works.
func (f *updateFlow) failedContent(err error) *qt.QWidget {
	closeBtn := qt.NewQPushButton3("Close")
	closeBtn.OnClicked(func() { f.dlg.Hide() })

	open := qt.NewQPushButton3("Open releases page")
	open.SetDefault(true)
	open.OnClicked(func() {
		f.dlg.Hide()
		_ = xopen.URL(releasesPageURL)
	})

	msg := "The update could not be downloaded. Nothing has changed — the app is " +
		"still running the version it started with.\n\n" + err.Error()
	return f.frame("Update failed", msg, nil, actionsRow(closeBtn, open))
}

// frame lays out the states identically — heading, body, an optional middle, and
// an optional action row — so the dialog does not jump as it moves between them.
func (f *updateFlow) frame(heading, body string, middle, actions *qt.QWidget) *qt.QWidget {
	root := qt.NewQWidget(nil)
	layout := qt.NewQVBoxLayout2()
	layout.SetContentsMargins(18, 18, 18, 18)
	layout.SetSpacing(12)

	layout.AddWidget(headingLabel(heading).QWidget)
	layout.AddWidget(bodyLabel(body).QWidget)
	if middle != nil {
		layout.AddWidget(middle)
	}
	layout.AddStretch()
	if actions != nil {
		layout.AddWidget(actions)
	}
	root.SetLayout(layout.QLayout)
	return root
}

// actionsRow right-aligns a row of buttons, mirroring the old
// container.NewHBox(layout.NewSpacer(), ...) pattern.
func actionsRow(buttons ...*qt.QPushButton) *qt.QWidget {
	root := qt.NewQWidget(nil)
	h := qt.NewQHBoxLayout2()
	h.SetContentsMargins(0, 0, 0, 0)
	h.AddStretch()
	for _, b := range buttons {
		h.AddWidget(b.QWidget)
	}
	root.SetLayout(h.QLayout)
	return root
}

// headingLabel is the bold sub-heading text at the top of every state.
func headingLabel(text string) *qt.QLabel {
	l := qt.NewQLabel3(text)
	f := l.Font()
	f.SetPointSize(15)
	f.SetBold(true)
	l.SetFont(f)
	return l
}

// bodyLabel is the wrapped, muted explanatory text under the heading.
func bodyLabel(text string) *qt.QLabel {
	l := qt.NewQLabel3(text)
	l.SetWordWrap(true)
	setRole(l, "caption")
	return l
}

// setRole sets (or clears, with role == "") the "role" dynamic property that
// uitheme's stylesheet keys its [role="..."] selectors on, then forces Qt to
// re-evaluate the stylesheet against this widget. Qt does NOT automatically
// repolish a widget when an arbitrary dynamic property changes — only the
// unpolish/polish pair below makes an attribute-selector style update take effect.
func setRole(w *qt.QLabel, role string) {
	w.SetProperty("role", qt.NewQVariant11(role))
	if s := w.Style(); s != nil {
		s.Unpolish(w.QWidget)
		s.Polish(w.QWidget)
	}
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
// that requires the app to be gone. dispatchQueue is the same queue the caller's
// updateFlow used for its prepare() step — Quit must run on the Qt UI thread.
func (a *app) finishUpdate(method update.Method, p update.Prepared, rel *update.Release, dispatchQueue *uidispatch.Queue) {
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
	dispatchQueue.Post(a.Quit)
}

// updatePromptContent is the OFFER state: what is installed, what is available,
// and the two answers.
//
// The two version numbers are the only thing being asked about, so they get a
// lined-up comparison in the monospace face rather than a sentence. It replaced a
// dialog.NewConfirm whose whole message was one string with an embedded newline.
func (a *app) updatePromptContent(rel *update.Release, done func(apply bool)) *qt.QWidget {
	root := qt.NewQWidget(nil)
	layout := qt.NewQVBoxLayout2()
	layout.SetContentsMargins(18, 18, 18, 18)
	layout.SetSpacing(12)

	layout.AddWidget(headingLabel("Update available").QWidget)
	layout.AddWidget(bodyLabel("Downloading happens now, with the VPN still connected. " +
		"OpenFortiTray only restarts once the update is ready to install.").QWidget)

	card := qt.NewQWidget(nil)
	form := qt.NewQFormLayout2()
	form.AddRow(mutedLabel("Installed").QWidget, monoLabel(version).QWidget)
	form.AddRow(mutedLabel("Available").QWidget, monoLabel(rel.Tag).QWidget)
	card.SetLayout(form.QLayout)
	layout.AddWidget(card)

	later := qt.NewQPushButton3("Later")
	later.OnClicked(func() { done(false) })
	apply := qt.NewQPushButton3("Download update")
	apply.SetDefault(true)
	apply.OnClicked(func() { done(true) })

	layout.AddStretch()
	layout.AddWidget(actionsRow(later, apply))

	root.SetLayout(layout.QLayout)
	return root
}

// mutedLabel is a key label for the small two-column card.
func mutedLabel(text string) *qt.QLabel {
	l := qt.NewQLabel3(text)
	setRole(l, "caption")
	return l
}

// monoLabel is a right-aligned monospace value, so version numbers line up
// digit-for-digit between the two rows.
func monoLabel(text string) *qt.QLabel {
	l := qt.NewQLabel3(text)
	l.SetAlignment(qt.AlignRight | qt.AlignVCenter)
	f := l.Font()
	f.SetStyleHint(qt.QFont__Monospace)
	f.SetFamily("monospace")
	l.SetFont(f)
	return l
}
