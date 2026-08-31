# OpenFortiTray: Fyne → Qt6 (miqt) Migration + UI Redesign — Design

## Context

Fyne's macOS GLFW driver has a confirmed, unfixable-in-place bug: `glfw.PollEvents()`
runs synchronously inside Fyne's single main-thread event loop and can block
forever after display power-cycling, freezing the whole app (tray clicks, clean
quit, everything) permanently. Root-caused via live `lldb` backtrace on a frozen
process; confirmed not already fixed upstream (closest issues: fyne-io/fyne
#5724, #2791, #6467/#6478 — none match this exact silent, non-panicking,
display-wake-triggered freeze). A watchdog workaround (external freeze
detection + self-relaunch) was built and verified working, but the user
rejected it as a permanent answer: the tool needs to be solid at the
foundation, not self-healing around a known-broken foundation.

Decision: replace Fyne entirely with Qt6 via the `miqt` Go bindings
(`github.com/mappu/miqt`, actively maintained; `therecipe/qt` is abandoned).
Confirmed via a real, running spike (`qt-spike/main.go` + `glass_qt_darwin.m`)
that the same native-vibrancy technique used for Fyne transfers directly:
`(*QWidget).WinId()` exposes the native handle (an `NSView*` on macOS, one
hop from `NSWindow*` via `.window`) and `Qt::WA_TranslucentBackground`
(`qt6.WA_TranslucentBackground`) is Qt's built-in equivalent of the
hand-rolled Fyne translucent-background trick. The spike builds, runs, and
renders a real `NSVisualEffectView`-backed window with a nav rail + status
content pane. User has reviewed this running mock and approved proceeding.

## Goal

Migrate OpenFortiTray's entire GUI layer (main window, settings, status,
system tray, update-flow dialogs, native glass/vibrancy) from Fyne to Qt6,
alongside a UI redesign that keeps the already-approved information
architecture (one window, one level of navigation — Status / Connection /
Advanced, three fewer modals, from the prior glass-UI redesign) but
re-expresses it with native Qt widgets and idioms. Simple over clever:
this redesign does not introduce new navigation depth, new screens, or new
concepts — it re-skins the existing approved IA on a sound framework.

## Scope

**In scope:**
- `cmd/openfortitray` GUI code: main window, settings screens, status view,
  tray icon/menu, update-flow dialogs, native vibrancy attachment per OS.
- `internal/tray`, `internal/status`, `internal/settings`, `internal/shell`,
  `internal/uitheme` — the five packages with direct Fyne dependencies today.
  Each gets a Qt-based rewrite of its rendering; package boundaries and
  public APIs are redesigned only where Fyne's widget model forced a shape
  that Qt doesn't need (see Package Changes below).
- Application entrypoint / event loop plumbing (`qt.NewQApplication`,
  `qt.QApplication_Exec()` replacing Fyne's `app.New()` / driver loop).

**Out of scope (unchanged, framework-agnostic, reused as-is):**
`internal/config`, `internal/tunnel`, `internal/ipsec`, `internal/update`,
`internal/credstore`, `internal/auth`, `internal/dns`, `internal/autostart`,
`internal/xopen`, `internal/uistate`. None of these import Fyne today; they
are pure business logic / OS integration and carry over untouched. This is
the majority of the codebase by both file count and complexity (the IPsec
runtime, the tunnel supervisor, the update/relaunch machinery) — the
migration's blast radius is the UI shell, not the VPN engine.

## Architecture

### Event loop & app lifecycle

Fyne's `fyne.Do`/`fyne.DoAndWait` cross-thread dispatch and its single GLFW
event loop go away entirely. Qt's own event loop (`QApplication::exec`) is
single-threaded by the same Qt convention every native Qt app already
follows: all widget mutation happens on the main thread. Background work
(tunnel supervisor callbacks, IPsec status updates) marshals onto the Qt
main thread via `QMetaObject::invokeMethod`-style dispatch — miqt exposes
this as connecting a `QTimer` polling a channel written by background
goroutines is a simpler correct alternative and does not need cgo-level
callback marshaling from an arbitrary goroutine, avoiding the class of bug
Fyne's watchdog was built to route around: instead of arbitrary goroutines
racing to interact with the toolkit, exactly one `QTimer` on the main
thread drains a `chan uiUpdate` written by the supervisors. This removes
the freeze surface entirely rather than detecting and recovering from it —
there is no synchronous cross-thread call into the toolkit for external
events (display wake, tunnel state changes) to block on.

### Package changes

- **`internal/tray`** (`tray.go`, `icons.go`, `badge.go`): rebuilt on
  `QSystemTrayIcon` + `QMenu`. Icon/badge compositing logic (drawing a
  colored dot over the base icon for connected/error states) is pure
  image manipulation today and is preserved; only the Fyne
  `fyne.Resource`/`desktop.TrayIcon` glue is replaced with
  `QIcon`/`QSystemTrayIcon.SetIcon`.
- **`internal/status`**: the connected/disconnected/connecting state
  render becomes a `QWidget` subtree (ring indicator, state label,
  gateway subtitle, IP/protocol detail card, disconnect button) — the
  same visual content as today's Fyne status view and the approved mock,
  rebuilt with `QLabel`/`QPushButton`/`QVBoxLayout`.
- **`internal/settings`**: `logic.go` (validation, dirty-tracking,
  per-profile field state) has no Fyne dependency in its actual logic —
  only `settings.go`'s render layer does. `logic.go` is preserved
  unchanged; `settings.go` is rewritten against `QLineEdit`/`QComboBox`/
  `QTabWidget` (or the nav-rail's own "Connection"/"Advanced" sections,
  matching the approved IA rather than reintroducing tabs).
- **`internal/shell`**: the window-chrome/shell layout (nav rail +
  content pane host) is rebuilt as a `QMainWindow` + `QHBoxLayout` of
  a fixed-width rail `QWidget` and a content `QStackedWidget` swapping
  between Status/Connection/Advanced panes — directly mirroring the
  mock's structure.
- **`internal/uitheme`**: Fyne's theme-override mechanism (colors, the
  translucent-background hack) is replaced by Qt stylesheets
  (`QWidget.SetStyleSheet`) plus `Qt::WA_TranslucentBackground`. Kept as
  its own package: one place owns the app's color tokens and stylesheet
  strings, consumed by every rewritten view.

### Native vibrancy per platform

Same `glass_*.go`/`.m`/`.h` per-OS file structure as today, same contract
(attach a native blur/acrylic surface behind the window's content), only
the native-handle source changes:

- **macOS**: `glass_darwin.m` rewritten against `WinId()` → `NSView*` →
  `.window` → `NSVisualEffectView`, proven in the spike.
- **Windows**: current `glass_windows.go` — confirm/extend to attach DWM
  Mica/Acrylic (`DwmSetWindowAttribute` with
  `DWMWA_SYSTEMBACKDROP_TYPE`) using the `HWND` from `WinId()` directly
  (Qt exposes the native `HWND` on Windows the same way Fyne's driver
  did) — no cross-hop needed, unlike macOS.
- **Linux**: current `glass_linux.go` is already a no-op/best-effort
  (no universal compositor blur API); unchanged in spirit — Qt's own
  theming is the fallback, no regression from today.

### Testing without Fyne's test driver

Fyne's `test` package (used for `render_capture_test.go` style structural
assertions) has no Qt equivalent in `miqt`. The rewritten UI packages drop
pixel/widget-tree capture tests in favor of testing the same thing the
`logic.go`/state-machine layers already test today: pure functions
(validation, dirty-tracking, tray icon state selection, theme token
selection) with no widget construction involved. Widget-construction code
itself (the `QWidget` tree assembly) is deliberately kept thin — a
straight-line translation of "state → which widgets/text/styles" — and
verified by running the app (per the `run` skill: launch the real binary,
drive it, screenshot it) rather than unit-testing Qt object graphs, which
would just be re-testing Qt itself.

## Risks

- **Cross-platform build verification.** Only macOS is available for
  direct interactive testing this session. Windows/Linux Qt cross-compiles
  must be verified via CI build (the existing `.github/workflows/release.yml`
  already cross-builds for all three) plus, where possible, a user-run
  smoke test before the release is trusted for those platforms.
- **cgo build complexity.** miqt requires `pkg-config` + Qt6 `.pc` files
  and `CGO_CXXFLAGS=-std=c++17` (Qt6 headers use C++17 `_v` trait
  aliases the default clang-cgo C++11 dialect doesn't have — hit and
  fixed during the spike). CI images need Qt6 installed
  (`brew install qt` / `apt install qt6-base-dev` / vcpkg-or-equivalent
  on Windows) and this env var set; this is new CI surface the release
  workflow doesn't have today for Fyne (which is pure-Go, no cgo).
- **Binary size / distribution.** Qt6 linking pulls in real shared
  library dependencies unlike Fyne's static Go binary; packaging
  (the macOS `.app` bundle, the Windows installer, the Linux package)
  needs to bundle or depend on Qt6 runtime libraries. This changes the
  release artifact shape and must be addressed in the plan, not deferred.

## Non-goals

- No new features. This is a framework swap + faithful re-expression of
  the already-approved simple IA, not a scope expansion.
- No incremental dual-framework period. The decision (user's, explicit)
  is a full switch — no Fyne/Qt toggle, no partial rollout by platform.
