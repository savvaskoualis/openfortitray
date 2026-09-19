package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/savvaskoualis/openfortitray/internal/update"
	"github.com/savvaskoualis/openfortitray/internal/xopen"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// pendingUpdate is what's been downloaded and is waiting for the user to
// confirm the restart, set once prepareUpdate succeeds and read once by
// Bridge.RestartAndInstall.
type pendingUpdate struct {
	method   update.Method
	prepared update.Prepared
	rel      *update.Release
}

// promptUpdate emits the update offer to the frontend, replacing the old
// Qt QDialog's offer state. The frontend (frontend/dist/status.js) shows a
// confirm() and, on yes, calls Bridge.DownloadUpdate.
func (a *app) promptUpdate(rel *update.Release) {
	ctx := a.ctxSnapshot()
	if ctx == nil {
		return
	}
	wailsruntime.EventsEmit(ctx, "update:offer", rel.Tag)
}

// prepareUpdate downloads the update with the app still running (replacing
// the old QDialog's "preparing" state — no progress event is emitted; a
// confirm()-based frontend flow has no progress bar to drive), then emits
// "update:ready" or "update:failed".
func (a *app) prepareUpdate(rel *update.Release) {
	method := update.InstallMethod()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		log.Printf("update: preparing %s via %s", rel.Tag, method)
		p, err := update.Prepare(ctx, method, a.downloaderFor(rel))
		if err != nil {
			log.Printf("update: prepare failed: %v", err)
			if wctx := a.ctxSnapshot(); wctx != nil {
				wailsruntime.EventsEmit(wctx, "update:failed", err.Error())
			}
			return
		}
		log.Printf("update: prepared; waiting for the user to restart")
		a.pendingUpdateMu.Lock()
		a.pendingUpdate = &pendingUpdate{method: method, prepared: p, rel: rel}
		a.pendingUpdateMu.Unlock()
		if wctx := a.ctxSnapshot(); wctx != nil {
			wailsruntime.EventsEmit(wctx, "update:ready", rel.Tag)
		}
	}()
}

// downloaderFor returns the Windows download step, or nil on the Homebrew
// path where brew does its own fetching. Unchanged from the old
// updateflow.go — pure logic, no Qt dependency.
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

// finishUpdate launches the detached updater and quits — the only part that
// requires the app to be gone. Unchanged in substance from the old
// updateflow.go; dropped the dispatchQueue parameter since Quit no longer
// needs cross-thread marshaling.
func (a *app) finishUpdate(method update.Method, p update.Prepared, rel *update.Release) {
	log.Printf("update: applying %s via %s", rel.Tag, method)
	if err := update.Apply(method, p.InstallerPath, os.Getpid()); err != nil {
		log.Printf("update: apply failed: %v; opening releases page", err)
		_ = xopen.URL(releasesPageURL)
		return
	}
	if a.shouldResumeAfterUpdate() {
		if err := writeResumeMarker(a.cfgDir); err != nil {
			log.Printf("update: could not write resume marker: %v", err)
		}
	}
	a.Quit()
}
