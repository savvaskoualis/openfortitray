package main

import (
	"context"
	"embed"

	"github.com/savvaskoualis/openfortitray/internal/uistate"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Bridge is the exported adapter Wails binds to the frontend. It holds no
// logic of its own — every method delegates to app, mirroring the existing
// tray.App/status.Host/settings.Host adapter pattern. app itself stays
// unexported; Wails requires an exported bound type, so Bridge is that type.
// Task 4 adds GetConfig/SaveConfig once their dependencies (settings.Validate,
// the settingsHost() accessor) exist.
type Bridge struct {
	a *app
}

func (b *Bridge) Connect()      { b.a.Connect() }
func (b *Bridge) Disconnect()   { b.a.Disconnect() }
func (b *Bridge) ShowSettings() { b.a.ShowSettings() }
func (b *Bridge) ShowStatus()   { b.a.ShowStatus() }
func (b *Bridge) Quit()         { b.a.Quit() }

func (b *Bridge) HideWindow() {
	if b.a.ctx != nil {
		wailsruntime.WindowHide(b.a.ctx)
	}
}

func (b *Bridge) IsDarkMode() bool { return isDarkMode() }

// DownloadUpdate starts downloading the release the user was offered
// ("update:offer"), replacing the old QDialog's "Download update" button.
func (b *Bridge) DownloadUpdate() {
	b.a.updateMu.Lock()
	rel := b.a.updateRel
	b.a.updateMu.Unlock()
	if rel == nil {
		return
	}
	b.a.prepareUpdate(rel)
}

// RestartAndInstall applies the update prepared by prepareUpdate,
// replacing the old QDialog's "Restart now" button.
func (b *Bridge) RestartAndInstall() {
	b.a.pendingUpdateMu.Lock()
	pu := b.a.pendingUpdate
	b.a.pendingUpdateMu.Unlock()
	if pu == nil {
		return
	}
	b.a.finishUpdate(pu.method, pu.prepared, pu.rel)
}

// CurrentView reports the live tunnel view for the frontend to render on
// load (before the first "tunnel:event"-style push exists — that wiring is a
// later task). uistate.ViewFor takes a tunnel.Event, not the tunnelParams
// a.snapshot() returns (those are the profile/paths the tunnel dials, not its
// live state), so this reads a.lastEventSnapshot() instead — the most recent
// event pump has seen, mu-guarded so it is safe to read from whatever
// goroutine Wails invokes a bound method on.
func (b *Bridge) CurrentView() uistate.View {
	return uistate.ViewFor(b.a.lastEventSnapshot())
}

// buildAppOptions is the testable seam between app/Bridge construction and
// wails.Run: it returns the options struct without invoking the webview, so
// its shape (frameless, fixed size, bound object) is unit-testable.
func buildAppOptions(a *app, assets embed.FS) *options.App {
	bridge := &Bridge{a: a}
	return &options.App{
		Title:             "OpenFortiTray",
		Width:             380,
		Height:            600,
		Frameless:         true,
		DisableResize:     true,
		HideWindowOnClose: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: func(ctx context.Context) {
			a.ctx = ctx
		},
		Bind: []interface{}{bridge},
	}
}
