# OpenFortiTray: Qt6 (miqt) → Wails Migration — Design

## Context

The Qt6/miqt UI, shipped across v0.2.1–v0.2.6, accumulated repeated
footgun costs during this migration: `WA_StyledBackground` silently
breaking widget rendering under the Fusion style, `windeployqt6` not
bundling Qt's own MSYS2-packaged third-party dependencies
(`libdouble-conversion.dll` et al., requiring a hand-rolled `objdump`
dependency-closure walker in CI), Windows DWM Acrylic requiring both
`DwmSetWindowAttribute` and the previously-missing
`DwmExtendFrameIntoClientArea` call to render at all, and a general "seems
old"/"childish, 90s" visual result even after a full styling pass
(Fusion + QSS + drop shadows). The user's own prior Fyne UI was judged
better-looking with less fighting. Decision: drop Qt entirely and rebuild
the UI layer on Wails (Go backend + native OS webview, HTML/CSS/JS
frontend), modeling the visual design directly on Tailscale's macOS/
Windows tray client — a minimal, single-window, no-chrome popover rather
than Qt's app-window conventions.

A working mock of both screens (Main status window, Settings) was built
as an interactive HTML/CSS/JS artifact and approved by the user before
this spec was written: https://claude.ai/artifact/JwDhoadtbhXFoU1vauQH6i

## Goal

Replace the entire Qt6/miqt UI layer with a Wails-based UI that:
- Visually matches the approved mock (380×600 frameless rounded popover,
  status ring + primary button + detail card + activity log, tray-icon
  positioned, no OS window chrome).
- Reuses 100% of existing backend logic (`internal/tunnel`, `internal/
  update`, `internal/autostart`, `internal/config`, etc.) unchanged.
- Removes the Qt-specific CI bundling machinery (`windeployqt6`, the
  MinGW/Qt6 DLL-closure walker, Mesa bundling) entirely rather than
  continuing to patch it.

## Scope

**In scope (deleted and rebuilt):**
- `internal/shell`, `internal/status`, `internal/uitheme` — Qt widget
  code, fully replaced by `frontend/` HTML/CSS/JS.
- `internal/uidispatch` — deleted; Wails' `runtime.EventsEmit` is
  goroutine-safe, making the manual dispatch queue unnecessary.
- `cmd/openfortitray/glass_darwin.m`, `glass_windows.go`, `glass.go`,
  `qtapp.go` — deleted; Wails' `options.App` (`Frameless`, `Mac.TitleBar`,
  `Windows.Theme`) covers window chrome/vibrancy configuration natively.
- `internal/tray` — the `QSystemTrayIcon`/`QMenu` construction is
  replaced with `github.com/energye/systray`; the event-mapping logic
  (which tray label/icon a given `tunnel.Event` produces) is preserved.
- `internal/settings` — `logic.go` (validation, dirty-tracking) is
  preserved unchanged; the Qt render layer is replaced by
  `frontend/settings.js` driving the Settings page markup.
- `go.mod`: `github.com/mappu/miqt` removed, `github.com/wailsapp/wails/
  v2` and `github.com/energye/systray` added.
- `.github/workflows/release.yml`: the "Bundle Qt6 runtime DLLs" and
  "Bundle transitive Qt6/MinGW DLL closure" steps, and the Homebrew Qt6
  bootstrap in `build-darwin`, are deleted (not adapted) and replaced
  with `wails build` per-platform invocations.
- `scripts/openfortitray.iss`: Qt6/Mesa `[Files]` entries replaced with
  the Wails-produced single binary plus a `WebView2Setup.exe`
  bootstrapper `Run` line.

**Out of scope (unchanged, reused as-is):**
`internal/config`, `internal/tunnel`, `internal/ipsec`, `internal/
update` (non-UI parts — `CaskChecker`, `DownloadAndVerify`, `Apply`),
`internal/credstore`, `internal/auth`, `internal/dns`, `internal/
autostart`, `internal/xopen`, `internal/uistate`, `internal/settings/
logic.go`. None of these import a UI toolkit today; all are pure Go
business/OS-integration logic. `main.go`'s `app` struct keeps its
`Connect`/`Disconnect`/`Config`/`Commit`/`ShowSettings`/`ShowStatus`/
`Quit` method bodies unchanged — only their exposure mechanism (interface
satisfaction → Wails-bound struct methods) changes.

## Architecture

### Frontend

Plain HTML/CSS/JS, no framework (React/Vue/etc. is unwarranted for 2
pages + a handful of DOM updates). Layout and visual tokens (colors,
radii, spacing) are taken directly from the approved mock's inline
styles, promoted to CSS custom properties in `frontend/main.css` for
light/dark theming:

```css
:root {
  --bg: #F7F8FA; --card-bg: #FFFFFF; --border: #E6E8EB;
  --text: #101828; --secondary: #667085; --accent: #2F6FED;
}
:root[data-theme="dark"] {
  --bg: #1C1D1F; --card-bg: #232427; --border: #333438;
  --text: #F2F2F3; --secondary: #9AA0A6;
}
```

`frontend/status.js` renders the Main page from a `tunnel.Event`-derived
JSON snapshot (state, gateway, IP, protocol, connected-since, recent
events); `frontend/settings.js` renders the Settings page from
`app.Config()`'s JSON and calls `app.Commit(cfg)` on Save. Both call
Wails-bound Go methods directly (`window.go.main.App.Connect()` etc.,
Wails' generated JS bindings) — no manual IPC protocol to design.

### Go ↔ JS event flow

`main.go`'s `a.pump()` loop (reads `tunnel.Event` off `a.events`) keeps
its exact shape; its three `Apply()` calls into `tray`/`status`/
`settings` become one:

```go
runtime.EventsEmit(a.ctx, "tunnel:event", uistate.From(ev))
```

`frontend/status.js` does `runtime.EventsOn("tunnel:event", render)` once
at load. `uistate.From` (existing, pure) already shapes a `tunnel.Event`
into the view fields the old Qt code consumed; it now gets JSON-marshaled
instead of driving `QLabel.SetText` calls — no changes to `internal/
uistate` itself.

### Window & tray

- `options.App{Frameless: true, Width: 380, Height: 600, ...}` — fixed
  size, no native chrome, matching the mock exactly.
- `github.com/energye/systray` builds the tray icon and right-click menu
  (Show / Connect|Disconnect / Settings / Check for Updates / Quit —
  same items `internal/tray` builds today). Left-click positions the
  Wails window near the tray icon (`runtime.WindowSetPosition`, computed
  from cursor position, clamped to the current monitor's work area) and
  toggles visibility.
- No in-window close button (frameless has none to mis-wire); dismissal
  is click-outside (a JS `window.onblur` → `runtime.WindowHide`) or
  Escape. Quit is tray-menu-only, structurally removing the close-to-tray
  regression class entirely rather than requiring an event handler to
  get right.
- Settings is a second page within the same window/frontend bundle
  (`Main.dc.html`→`Settings.dc.html`-equivalent, i.e. a `#settings` view
  toggled by JS), not a second OS window — one frameless-window lifecycle
  to manage, matching the mock's single-window navigation.

### Native window options per platform

- **macOS**: WKWebView is OS-builtin; no vibrancy is used (the mock's
  look is a flat card with its own shadow, not blur) — `mac.TitleBar:
  mac.TitleBarHiddenInset` is unnecessary since the window is already
  frameless. No custom Objective-C glue needed; `glass_darwin.m` is
  deleted outright, not ported.
- **Windows**: WebView2 (prebundled on Win10 19H1+/Win11; the installer
  gains a bootstrapper `Run` line for older systems). No DWM Acrylic/Mica
  calls — the mock's flat-card-with-shadow look doesn't need them, so
  `glass_windows.go`'s `DwmSetWindowAttribute`/`DwmExtendFrameIntoClientArea`
  code is deleted, not ported. Dark-titlebar detection
  (`darkmode_windows.go`) is kept, now feeding `data-theme` on `<html>`
  instead of a QSS variable.
- **Linux**: `webkit2gtk-4.1` runtime dependency, documented alongside
  the existing `openconnect` binary dependency in install docs; CI's
  Linux build step gains `apt-get install libwebkit2gtk-4.1-dev`.

### Build & CI

`wails build -platform darwin/arm64` (etc., one invocation per target)
replaces `go build` + all Qt bundling steps. Wails embeds `frontend/` via
`//go:embed` and produces one binary — no DLL closure to walk, no Mesa to
bundle. The entire "Bundle Qt6 runtime DLLs" and "Bundle transitive Qt6/
MinGW DLL closure" steps in `release.yml`, and the Homebrew Qt6 bootstrap
in `build-darwin`, are deleted. Windows installer (`scripts/
openfortitray.iss`) `[Files]` section collapses to the single Wails
binary plus a `WebView2Setup.exe` bootstrapper.

### Testing

`internal/*` backend package tests are entirely unaffected — none import
a UI toolkit, before or after. New: one `chromedp`-driven smoke test
(per the `run` skill's webview-app pattern) launching the built binary,
driving a Connect click, and asserting the DOM reflects a state change —
replacing the "run the real binary, screenshot it" manual verification
Qt widget code relied on, with something CI can run unattended.

## Risks

- **Webview runtime dependency.** Unlike Qt's fully-bundled binary,
  Windows needs WebView2 (near-universal already, small bootstrapper for
  the remainder) and Linux needs `webkit2gtk-4.1` (already present on
  most desktop environments) — a new class of dependency Qt didn't have,
  accepted explicitly by the user as a tradeoff for visual fidelity and
  less framework-fighting.
- **Cross-platform verification.** Only macOS is available for direct
  interactive testing this session, same constraint as the Qt migration
  had. Windows/Linux Wails builds are verified via CI plus a user-run
  smoke test before trusting a release for those platforms — the same
  process that caught the Qt DLL and Acrylic bugs, now applied to a
  simpler build with fewer places to hide a bug.
- **Tray-relative window positioning.** No cross-platform library gives
  "position a window at the tray icon" for free; the clamped
  cursor-position calculation is hand-rolled and needs real verification
  on multi-monitor Windows/Linux setups, not just macOS.

## Non-goals

- No new features — this is a framework swap plus a closer re-expression
  of the approved Tailscale-modeled mock, not a scope expansion.
- No incremental dual-framework period — full switch, no Qt/Wails
  toggle, matching the same all-at-once approach the Fyne→Qt migration
  used.
- No React/Vue/build-step frontend tooling — plain HTML/CSS/JS is
  sufficient for a 2-page UI and avoids introducing an npm build
  pipeline this project has never had.
