package main

import (
	"context"
	"embed"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/settings"
	"github.com/savvaskoualis/openfortitray/internal/uistate"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Bridge is the exported adapter Wails binds to the frontend. It holds no
// logic of its own — every method delegates to app, mirroring the existing
// tray.App/status.Host/settings.Host adapter pattern. app itself stays
// unexported; Wails requires an exported bound type, so Bridge is that type.
type Bridge struct {
	a *app
}

// GetConfig returns a copy of the live configuration for the frontend to
// render and edit. The frontend edits its own copy and only writes back
// through SaveConfig.
func (b *Bridge) GetConfig() config.Config {
	return *b.a.settingsHost().Config()
}

// SaveConfig validates cfg and, if valid, commits it as the live
// configuration. It returns nil on success or a *settings.Issue describing
// what is wrong — either a validation failure or a Commit error (e.g.
// autostart/persist failure) reported the same way so the frontend has one
// error shape to handle.
func (b *Bridge) SaveConfig(cfg config.Config) *settings.Issue {
	if issue := settings.Validate(&cfg); issue != nil {
		return issue
	}
	if err := b.a.settingsHost().Commit(&cfg); err != nil {
		return &settings.Issue{Message: err.Error()}
	}
	return nil
}

func (b *Bridge) Connect()    { b.a.Connect() }
func (b *Bridge) Disconnect() { b.a.Disconnect() }

// ShowSettings/ShowStatus/Quit are deliberately NOT exposed on Bridge: the
// tray drives all three directly through app (see tray.Setup's App
// interface and cmd/openfortitray's onTrayClick), and no frontend code ever
// calls them. Quit in particular is a full-teardown method; there is no
// reason to reach it from the least-trusted boundary (arbitrary frontend JS)
// when nothing legitimate needs to.

func (b *Bridge) HideWindow() {
	if ctx := b.a.ctxSnapshot(); ctx != nil {
		wailsruntime.WindowHide(ctx)
	}
	b.a.setWindowVisible(false)
}

func (b *Bridge) IsDarkMode() bool { return isDarkMode() }

// GatewayLabel and DTLSLabel expose the status details card's Gateway/
// Protocol rows (frontend/dist/status.js' #d-gateway/#d-protocol), replacing
// the dead-code-only Go accessors left over from the deleted Qt status
// controller.
func (b *Bridge) GatewayLabel() string { return b.a.GatewayLabel() }
func (b *Bridge) DTLSLabel() string    { return b.a.DTLSLabel() }

// Version returns the build version string (main.version, stamped via
// -ldflags at build time) for the frontend footer to render instead of a
// hardcoded string.
func (b *Bridge) Version() string { return b.a.Version() }

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

// RecentActivity returns the status window's activity log, newest first, for
// the frontend to render on load and after every "tunnel:event" push.
// a.recentActivity() is mu-guarded, so this is safe to call from whatever
// goroutine Wails dispatches a bound JS call on.
func (b *Bridge) RecentActivity() []uistate.Entry {
	return b.a.recentActivity()
}

// buildAppOptions is the testable seam between app/Bridge construction and
// wails.Run: it returns the options struct without invoking the webview, so
// its shape (frameless, fixed size, bound object) is unit-testable.
func buildAppOptions(a *app, assets embed.FS) *options.App {
	bridge := &Bridge{a: a}
	return &options.App{
		// Title must stay in sync with position.go's windowTitle constant —
		// tray.SetWindowPosition finds this exact window natively on
		// macOS/Windows by title, so a drift here would silently break
		// cursor-relative positioning (falling back to Wails' own
		// screen-relative WindowSetPosition, not a crash — see
		// positionWindow's doc comment).
		Title:             windowTitle,
		Width:             windowW,
		Height:            windowH,
		Frameless:         true,
		DisableResize:     true,
		HideWindowOnClose: true,
		// StartHidden keeps the window from popping up unasked at every
		// launch — this is a tray app: it should only appear on a deliberate
		// tray click (see main.go's dock-activation comment for the same
		// design intent, carried over from the Qt era).
		StartHidden: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: func(ctx context.Context) {
			a.setCtx(ctx)
		},
		Bind: []interface{}{bridge},
	}
}
