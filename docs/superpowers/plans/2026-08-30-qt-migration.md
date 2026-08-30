# Fyne → Qt6 (miqt) Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace OpenFortiTray's Fyne GUI layer with Qt6 via `miqt`, eliminating the
confirmed-unfixable macOS GLFW event-loop freeze, while re-expressing the
already-approved simple IA (nav rail + Status/Connection/Advanced content pane)
with native Qt widgets.

**Architecture:** Bottom-up rewrite of the five Fyne-dependent packages
(`uitheme`, `shell`, `status`, `tray`, `settings`) plus `cmd/openfortitray`'s
app shell, glass/vibrancy attachment, and update-flow dialogs. A new
`internal/uidispatch` package replaces `fyne.Do`/`fyne.DoAndWait` with a
channel drained by a `QTimer` on the Qt main thread — this removes the freeze
surface rather than detecting it, so the freeze watchdog built earlier this
session is deleted, not ported. All ten framework-agnostic backend packages
(`config`, `tunnel`, `ipsec`, `update`, `credstore`, `auth`, `dns`,
`autostart`, `xopen`, `uistate`) are untouched.

**Tech Stack:** Go, `github.com/mappu/miqt` v0.14.0 (`qt6` subpackage), Qt6
(Homebrew on macOS, `qt6-base-dev` on Linux, MSYS2 UCRT64 `qt6-base` on
Windows), cgo with `CGO_CXXFLAGS=-std=c++17`.

**Spec:** `docs/superpowers/specs/2026-08-30-qt-migration-design.md`

## Global Constraints

- Qt dependency is exactly `github.com/mappu/miqt/qt6` v0.14.0 (`go get
  github.com/mappu/miqt/qt6@v0.14.0`). Import alias `qt "github.com/mappu/miqt/qt6"`
  everywhere, matching the proven spike.
- Every `go build`/`go vet`/`go test` invocation on this codebase from Task 2
  onward requires `CGO_CXXFLAGS=-std=c++17` in the environment (Qt6 headers use
  C++17 `std::is_*_v` trait aliases; the default cgo C++ dialect is C++11 and
  fails to compile without this flag — confirmed empirically in the pre-plan
  spike). Every task's test/build commands below already include it; do not
  drop it.
- `internal/config`, `internal/tunnel`, `internal/ipsec`, `internal/update`,
  `internal/credstore`, `internal/auth`, `internal/dns`, `internal/autostart`,
  `internal/xopen`, `internal/uistate`, and `internal/settings/logic.go` are
  byte-for-byte off-limits. No task may modify them. If a task's author
  believes one of these needs a change, that is a plan defect — rule on it
  per the SDD skill's ruling process and record why, rather than editing
  silently.
- No dual-framework runtime toggle, no build tag choosing Fyne vs Qt. The cut
  happens entirely in Task 10; before that, `cmd/openfortitray` will not
  successfully `go build` as a whole binary — that is expected. Tasks 1, 3-9
  are verified by building/testing their own package only
  (`go test ./internal/<pkg>/...`), never the full `./...`. Do not treat a
  broken top-level `go build ./...` before Task 10 as a regression.
- No Fyne import (`fyne.io/...`) may exist anywhere in the tree after Task 11.
- Widget-construction code stays thin and mechanical: "given this piece of
  state, which widgets/text/styles" — no business logic lives in a rewritten
  UI file that isn't already tested by `logic.go`, `uistate.go`, or the
  package's own new pure-logic helpers. Verify via running the real binary
  (per the `run` skill), not via Qt widget-tree unit tests — Qt object graphs
  are not the thing worth testing; the pure selection logic feeding them is.
- Every rewritten file keeps its exported API shape (types, constructor,
  method names) from the Fyne version except where a type changes from a Fyne
  type to its Qt equivalent (`fyne.CanvasObject` → `*qt.QWidget`,
  `fyne.Window` → `*qt.QMainWindow`, etc.) or where this plan explicitly says
  to drop something. This keeps `cmd/openfortitray`'s eventual rewiring
  (Task 10) a mechanical type-swap, not a redesign.

---

### Task 1: `internal/uidispatch` — main-thread dispatch queue

**Files:**
- Create: `internal/uidispatch/uidispatch.go`
- Test: `internal/uidispatch/uidispatch_test.go`

**Interfaces:**
- Produces: `uidispatch.New() *Queue`, `(*Queue).Post(fn func())`,
  `(*Queue).PostAndWait(fn func())`, `(*Queue).Drain()`. Every later task that
  currently calls `fyne.Do(f)` calls `q.Post(f)` instead; every
  `fyne.DoAndWait(f)` call becomes `q.PostAndWait(f)`. Task 10 owns the single
  `*uidispatch.Queue` instance and the `QTimer` that calls `Drain()`.

This package is pure Go — no Qt import, no cgo, fully testable without a
display or a `QApplication`.

- [ ] **Step 1: Write the failing tests**

```go
package uidispatch

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostRunsOnDrain(t *testing.T) {
	q := New()
	var ran atomic.Bool
	q.Post(func() { ran.Store(true) })
	if ran.Load() {
		t.Fatal("Post must not run synchronously")
	}
	q.Drain()
	if !ran.Load() {
		t.Fatal("Drain must run the posted func")
	}
}

func TestDrainRunsInPostOrder(t *testing.T) {
	q := New()
	var order []int
	var mu sync.Mutex
	for i := 0; i < 5; i++ {
		i := i
		q.Post(func() {
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
		})
	}
	q.Drain()
	for i, v := range order {
		if v != i {
			t.Fatalf("order = %v, want 0..4 in sequence", order)
		}
	}
}

func TestDrainWithNothingPostedIsANoop(t *testing.T) {
	q := New()
	q.Drain() // must not panic or block
}

func TestPostAndWaitBlocksUntilDrained(t *testing.T) {
	q := New()
	var ran atomic.Bool
	done := make(chan struct{})
	go func() {
		q.PostAndWait(func() { ran.Store(true) })
		close(done)
	}()

	// PostAndWait's caller must still be blocked before Drain runs.
	time.Sleep(20 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("PostAndWait returned before Drain ran the func")
	default:
	}

	q.Drain()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PostAndWait did not unblock after Drain")
	}
	if !ran.Load() {
		t.Fatal("PostAndWait's func did not run")
	}
}

func TestPostIsSafeFromManyGoroutines(t *testing.T) {
	q := New()
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.Post(func() { count.Add(1) })
		}()
	}
	wg.Wait()
	q.Drain()
	if count.Load() != 100 {
		t.Fatalf("count = %d, want 100", count.Load())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/uidispatch/... -v`
Expected: FAIL — package `uidispatch` does not exist yet.

- [ ] **Step 3: Write the implementation**

```go
// Package uidispatch marshals work from background goroutines onto a
// single-threaded UI loop. It replaces Fyne's fyne.Do/fyne.DoAndWait: instead
// of an arbitrary goroutine calling synchronously into the toolkit (the
// mechanism a blocking glfw.PollEvents call could wedge forever), every
// UI mutation is queued here and drained by a QTimer on the Qt main thread.
// There is no code path where an external event (display wake, a tunnel
// callback) blocks on a call into Qt itself.
package uidispatch

import "sync"

// Queue is a FIFO of pending UI-thread work. The zero value is not usable;
// construct with New.
type Queue struct {
	mu  sync.Mutex
	fns []func()
}

// New returns an empty Queue.
func New() *Queue {
	return &Queue{}
}

// Post queues fn to run on the next Drain. Safe to call from any goroutine.
// Never blocks.
func (q *Queue) Post(fn func()) {
	q.mu.Lock()
	q.fns = append(q.fns, fn)
	q.mu.Unlock()
}

// PostAndWait queues fn and blocks the calling goroutine until a Drain has
// actually run it. Callers must never call this from the same thread that
// calls Drain (that would deadlock) — it exists for OS-notification
// callbacks (display wake, signal handlers) that need the UI mutation to be
// visibly complete before they return control to the OS.
func (q *Queue) PostAndWait(fn func()) {
	done := make(chan struct{})
	q.Post(func() {
		fn()
		close(done)
	})
	<-done
}

// Drain runs every function queued since the last Drain, in the order they
// were posted. Must only be called from the UI thread (the QTimer callback).
func (q *Queue) Drain() {
	q.mu.Lock()
	fns := q.fns
	q.fns = nil
	q.mu.Unlock()

	for _, fn := range fns {
		fn()
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/uidispatch/... -race -v`
Expected: PASS, all 5 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/uidispatch/
git commit -m "feat(uidispatch): add main-thread dispatch queue for the Qt migration"
```

---

### Task 2: Build plumbing — miqt dependency, CI/release workflows, Qt entrypoint seed

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`
- Modify: `Makefile`
- Modify: `scripts/openfortitray.iss`
- Modify: `scripts/install.sh`
- Create: `cmd/openfortitray/qtapp.go`
- Test: `cmd/openfortitray/qtapp_test.go`

**Interfaces:**
- Produces: `newQApplication(args []string) *qt.QApplication`,
  `execQApplication() int`. Task 10 calls these instead of
  `fyneapp.NewWithID(...)` / `a.fyneApp.Run()`.
- Consumes: nothing from other tasks.

This task's real purpose is proving the whole cgo+Qt6 toolchain links
successfully on all three CI runners *before* any real UI code depends on it,
so later tasks get fast cross-platform build signal on every push instead of
discovering a Windows/Linux-only breakage at the very end.

- [ ] **Step 1: Add the miqt dependency**

```bash
CGO_CXXFLAGS=-std=c++17 go get github.com/mappu/miqt/qt6@v0.14.0
```

This updates `go.mod`/`go.sum`. Do not run `go mod tidy` yet — Fyne is still
a real dependency until Task 11.

- [ ] **Step 2: Write `cmd/openfortitray/qtapp.go`**

```go
package main

import qt "github.com/mappu/miqt/qt6"

// newQApplication constructs the one QApplication instance for the process.
// os.Args is passed through so Qt can strip its own recognized flags
// (-style, -platform, etc.) before the rest of main() sees argv.
func newQApplication(args []string) *qt.QApplication {
	return qt.NewQApplication(args)
}

// execQApplication runs the Qt event loop until quit; returns Qt's exit code.
func execQApplication() int {
	return qt.QApplication_Exec()
}
```

- [ ] **Step 3: Write the test**

```go
package main

import (
	"os"
	"testing"
)

// TestNewQApplicationConstructs proves the cgo+Qt6 toolchain actually links:
// if CGO_CXXFLAGS or the Qt6 pkg-config setup is broken, this package fails
// to compile/link long before this test body runs. It does not call
// execQApplication (that blocks forever without a Quit) or exercise any
// window — see the plan's Global Constraints on what's worth testing here.
func TestNewQApplicationConstructs(t *testing.T) {
	app := newQApplication(os.Args)
	if app == nil {
		t.Fatal("newQApplication returned nil")
	}
}
```

- [ ] **Step 4: Verify it builds and passes locally**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./cmd/openfortitray/... -run TestNewQApplicationConstructs -v`
Expected: this single test passes even though the rest of the package's
tests may currently fail to build against old Fyne-typed code the surrounding
files reference — if the whole package fails to compile due to unrelated
pre-existing files, that's fine per Global Constraints; confirm specifically
that adding `qtapp.go` compiles by running:
`CGO_CXXFLAGS=-std=c++17 go build ./... 2>&1 | grep -v "cmd/openfortitray"`
(this should show no NEW errors outside cmd/openfortitray, since qtapp.go is
additive and doesn't touch existing Fyne code paths).

- [ ] **Step 5: Update `Makefile`**

Read the current `Makefile` first (`cat Makefile`) to find its `build`/`test`/`vet`
targets. Add `CGO_CXXFLAGS=-std=c++17` to the environment of every target that
runs `go build`, `go vet`, or `go test` — e.g. if a target reads:

```makefile
test:
	go test ./...
```

change it to:

```makefile
test:
	CGO_CXXFLAGS=-std=c++17 go test ./...
```

Apply the same edit to every such target (`build`, `vet`, `lint` if it shells
out to `go vet`, etc.). Do not reformat targets that don't invoke the Go
toolchain.

- [ ] **Step 6: Update `.github/workflows/ci.yml`**

Three changes, one per OS branch in the existing `test` job:

Replace the Linux `Install GL/X11 build deps` step:

```yaml
      - name: Install Qt6 build deps
        if: runner.os == 'Linux'
        run: |
          sudo apt-get update
          sudo apt-get install -y gcc pkg-config qt6-base-dev
```

Add a macOS step (there is currently no macOS-specific dependency step —
add one right after "Set up Go"):

```yaml
      - name: Install Qt6 (Homebrew)
        if: runner.os == 'macOS'
        run: |
          brew install qt pkg-config
```

Change the Windows `msys2/setup-msys2` step from `MINGW64` to `UCRT64` and
add `qt6-base` (miqt's README documents UCRT64 + `qt6-base` as a tested
native-Windows recipe):

```yaml
      - name: Set up MinGW-w64 + Qt6 (MSYS2 UCRT64)
        if: runner.os == 'Windows'
        id: msys2
        uses: msys2/setup-msys2@66cd2cce69caa17b53920067426061ca1de3a884 # v2.32.0
        with:
          msystem: UCRT64
          install: mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-pkg-config mingw-w64-ucrt-x86_64-qt6-base
```

and update the very next step, "Put MinGW-w64 gcc on PATH", to point at the
UCRT64 bin dir instead of MINGW64:

```yaml
      - name: Put MinGW-w64 gcc on PATH
        if: runner.os == 'Windows'
        run: |
          printf '%s\n' "${{ steps.msys2.outputs.msys2-location }}\\ucrt64\\bin" >> "$GITHUB_PATH"
```

Finally, add `CGO_CXXFLAGS: -std=c++17` to the `test` job's top-level `env:`
block, alongside the existing `CGO_ENABLED: '1'`:

```yaml
    env:
      CGO_ENABLED: '1'
      CGO_CXXFLAGS: '-std=c++17'
```

Leave every other step (`gofmt`, `go vet`, `go test -race`, `go build`, the
Windows manifest/rsrc step, the shell syntax check) unchanged — they already
pick up the job-level `env` block.

- [ ] **Step 7: Update `.github/workflows/release.yml`**

Add the equivalent `env: CGO_CXXFLAGS: '-std=c++17'` to each of the three
build jobs' `env:` blocks (macOS at line ~41, Linux at line ~130, Windows at
line ~182 — search for `CGO_ENABLED: '1'` in this file, there are exactly
three matches, add the new line next to each).

For the Linux job, replace its `apt-get install -y gcc libgl1-mesa-dev
xorg-dev libwayland-dev libxkbcommon-dev wayland-protocols` line with:

```yaml
sudo apt-get install -y gcc pkg-config qt6-base-dev
```

For the Windows job, apply the same `MINGW64`→`UCRT64` +
`mingw-w64-ucrt-x86_64-{gcc,pkg-config,qt6-base}` change as ci.yml (same
`msys2/setup-msys2` step shape), and update its "prepend to PATH" step the
same way (`\ucrt64\bin` instead of `\mingw64\bin`).

For the **macOS job**, this needs more than `brew install qt`: the job
builds both `GOARCH=arm64` and `GOARCH=amd64` binaries on a `macos-14`
(Apple Silicon) runner for a universal-ish release, but Homebrew's `qt`
bottle is arch-native (arm64-only under `/opt/homebrew` on that runner).
Building the amd64 leg needs a second, x86_64 Homebrew prefix's Qt install.
`macos-14` GitHub runners ship a parallel x86_64 Homebrew at `/usr/local`
under Rosetta. Add, before the existing two `go build` lines:

```yaml
      - name: Install Qt6 (arm64 + x86_64, for both build legs)
        run: |
          brew install qt pkg-config
          arch -x86_64 /usr/local/bin/brew install qt pkg-config
```

Then change the two existing build lines to set `PKG_CONFIG_PATH` explicitly
per architecture, since two Qt installs now exist on the runner and
pkg-config's default search must not pick the wrong one for each leg:

```yaml
PKG_CONFIG_PATH="$(/opt/homebrew/bin/brew --prefix qt)/lib/pkgconfig" \
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
go build -ldflags="-s -w -X main.version=${GITHUB_REF_NAME}" -o dist/openfort... # (keep the rest of the existing line unchanged)

PKG_CONFIG_PATH="$(arch -x86_64 /usr/local/bin/brew --prefix qt)/lib/pkgconfig" \
CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
go build -ldflags="-s -w -X main.version=${GITHUB_REF_NAME}" -o dist/openfort... # (keep the rest of the existing line unchanged)
```

Read the actual current lines 68-71 of `release.yml` first and preserve
every existing flag/path in `-o dist/openfort...` — only prepend the new
`PKG_CONFIG_PATH=...` assignment to each of the two `go build` invocations.

- [ ] **Step 8: Bundle the Qt6 runtime into every distributable**

Unlike Fyne (a static Go binary), Qt6 links against real shared libraries at
runtime. Every distributable this repo ships must either bundle those
libraries or declare them as an install-time dependency — this was flagged
as a risk in the design spec and must be closed here, not deferred, since
Tasks 3-10 will otherwise produce a binary that only runs on machines that
happen to have the exact Qt6 the developer used.

**macOS (`Makefile`'s `app` target):** Qt's own `macdeployqt` tool copies the
needed `.framework` bundles into `Contents/Frameworks` and rewrites the
binary's load commands to find them there — this must run **before** the
existing ad-hoc `codesign` step (codesigning after `macdeployqt` modifies the
binary would invalidate the signature; read the Makefile's own comment on
this ordering hazard right above the codesign step and preserve it). Add,
right after the existing `cp $(BIN) $(APP_BUNDLE)/Contents/MacOS/$(BIN)` line
and before the icon-generation block:

```makefile
	@QT_PREFIX="$$(brew --prefix qt 2>/dev/null)"; \
	if [ -n "$$QT_PREFIX" ] && [ -x "$$QT_PREFIX/bin/macdeployqt" ]; then \
		"$$QT_PREFIX/bin/macdeployqt" "$(APP_BUNDLE)"; \
		echo "make app: bundled Qt6 frameworks via macdeployqt"; \
	else \
		echo "make app: macdeployqt not found at $$QT_PREFIX/bin — the .app will only run on machines with a matching Qt6 install" >&2; \
		exit 1; \
	fi
```

Confirm the tool is actually named `macdeployqt` (not `macdeployqt6`) in the
installed Homebrew `qt` formula before relying on this — `ls
"$(brew --prefix qt)/bin" | grep deployqt` — and adjust the name in the
Makefile snippet above if it differs.

**Windows (`release.yml`'s Windows job):** immediately after the existing
`go build ... -o dist/openfortitray-windows-amd64.exe` step and its
`opengl32.dll`/Mesa-bundling step, add a `windeployqt6` step that copies the
required Qt DLLs and platform plugin next to the exe (same `dist/` layout
the Mesa DLL bundling already uses):

```yaml
      - name: Bundle Qt6 runtime DLLs
        run: |
          "${{ steps.msys2.outputs.msys2-location }}\ucrt64\bin\windeployqt6.exe" --release --no-translations dist\openfortitray-windows-amd64.exe
```

Verify the exact binary name (`windeployqt6.exe` vs `windeployqt.exe`) under
the UCRT64 `bin` directory once Task 2's Step 6 MSYS2/Qt6 install is in
place — list `mingw-w64-ucrt-x86_64-qt6-base`'s installed files if unsure.

Then update `scripts/openfortitray.iss`'s `[Files]` section (read it in full
first) to package everything `windeployqt6` drops next to the exe, not just
the exe itself — add a wildcard entry alongside the existing openconnect/Mesa
bundling entries, e.g. a `Source: "..\dist\platforms\*"; DestDir:
"{app}\platforms"` and `Source: "..\dist\Qt6*.dll"; DestDir: "{app}"` pair
(match the exact relative paths `windeployqt6` actually produces — it may
nest platform plugins under `dist\platforms\` or emit them flat; confirm by
running it once and listing `dist/` before writing the final `.iss` paths).

**Linux (`scripts/install.sh`):** add the Qt6 runtime package to the same
package-manager dependency block that already installs `openconnect` (around
line 426) — read that block's exact `if command -v apt-get ...` /
`elif dnf` / `elif pacman` structure and extend each branch with the Qt6
runtime package for that distro family, matching the CI/release install's
package choice for consistency (`qt6-base-dev` on apt-based systems is
heavier than a minimal runtime-only package but matches what CI already
installs, so a version mismatch between build-time and install-time Qt6 is
less likely):

```bash
if command -v apt-get >/dev/null 2>&1; then sudo apt-get install -y openconnect qt6-base-dev
elif command -v dnf >/dev/null 2>&1; then sudo dnf install -y openconnect qt6-qtbase-devel
elif command -v pacman >/dev/null 2>&1; then sudo pacman -S --noconfirm openconnect qt6-base
```

(Illustrative — edit the real existing lines in place rather than
duplicating them; preserve every other flag/argument the current lines
already pass.)

- [ ] **Step 9: Commit**

```bash
gofmt -l cmd internal
git add go.mod go.sum Makefile scripts/openfortitray.iss scripts/install.sh .github/workflows/ci.yml .github/workflows/release.yml cmd/openfortitray/qtapp.go cmd/openfortitray/qtapp_test.go
git commit -m "build: add miqt/Qt6 toolchain and runtime bundling across CI, release, and local builds"
```

Note for the implementer: this commit will not make `go build ./...` for
`cmd/openfortitray` succeed as a whole binary yet (per Global Constraints) —
`qtapp.go` is additive only. CI's `go build ./...` step **will** fail on this
push until Task 10 finishes wiring; this is the one task in the plan where
that's true for the CI run itself, not just local dev. Note this expectation
in the task's completion report so the reviewer doesn't flag a red CI run as
a regression.

---

### Task 3: `internal/uitheme` rewrite — Qt stylesheet token map

**Files:**
- Modify: `internal/uitheme/uitheme.go` (full rewrite)
- Modify: `internal/uitheme/uitheme_test.go` (full rewrite)

**Interfaces:**
- Produces: `uitheme.Tokens` (a struct of every color/size value, both light
  and dark), `uitheme.StyleSheet(dark bool) string` (returns a complete QSS
  string built from the tokens, ready for
  `(*qt.QWidget).SetStyleSheet(uitheme.StyleSheet(dark))`),
  `uitheme.BackgroundColor(dark bool) (r, g, b, a uint8)` (the one
  alpha-bearing token, needed directly by the glass-attach code in Task 8 to
  set a matching translucent Qt background — QSS alone can't be queried back
  out by the ObjC/Win32 glue).
- Consumes: nothing new. `dark` is decided by the caller in Task 10
  (`qt.QGuiApplication` exposes a way to read the OS color scheme — Task 10's
  author greps for it at wiring time; this task hard-codes both tables and
  leaves the caller to choose).

**Token values** (copied exactly from the current Fyne implementation — do
not invent new colors):

| Token | Light | Dark |
|---|---|---|
| Background | `#F6F7F9` @ alpha `0x40` | `#16181C` @ alpha `0x40` |
| HeaderBackground | `#FFFFFF` | `#1E2128` |
| MenuBackground | `#FFFFFF` | `#1E2128` |
| OverlayBackground | `#FFFFFF` | `#1E2128` |
| Foreground | `#171A1F` | `#EDEFF2` |
| PlaceHolder | `#5C6470` | `#9AA2AE` |
| Disabled | `#5C6470` | `#9AA2AE` |
| Separator | `#E2E5EA` | `#2C313A` |
| InputBorder | `#E2E5EA` | `#2C313A` |
| Primary | `#2F6FEB` | `#5B93F5` |
| ForegroundOnPrimary | `#FFFFFF` | `#0E1013` |
| InputBackground | `#FFFFFF` | `#22262E` |
| Button | `#FFFFFF` | `#22262E` |
| Success | `#2E9E5B` | `#41BE77` |
| Warning | `#B87514` | `#E0A140` |
| Error | `#C4362F` | `#E86A62` |
| Hover (alpha-only wash, no RGB in light) | `rgba(0,0,0,0x10)` | `rgba(0xFF,0xFF,0xFF,0x12)` |

Sizes (copied exactly): Text=13, CaptionText=11, SubHeadingText=15,
HeadingText=20, Padding=5, InnerPadding=10, CardRadius=8, ButtonRadius=6,
InputRadius=6, SeparatorThickness=1.

These are copied verbatim from the current `internal/uitheme/uitheme.go`'s
`lightColors`/`darkColors`/`sizes` maps — every value in the table above is
final; there is nothing left to transcribe.

- [ ] **Step 1: Write the failing test**

```go
package uitheme

import (
	"strings"
	"testing"
)

func TestBackgroundColorIsTranslucentInBothModes(t *testing.T) {
	for _, dark := range []bool{false, true} {
		_, _, _, a := BackgroundColor(dark)
		if a == 0xFF {
			t.Fatalf("BackgroundColor(dark=%v) alpha = 0xFF, want translucent (<0xFF) — Qt's WA_TranslucentBackground needs a non-opaque background to let native vibrancy show through", dark)
		}
		if a == 0x00 {
			t.Fatalf("BackgroundColor(dark=%v) alpha = 0x00, want non-zero — fully transparent would make Qt's own content invisible too", dark)
		}
	}
}

func TestStyleSheetContainsCoreTokens(t *testing.T) {
	for _, dark := range []bool{false, true} {
		ss := StyleSheet(dark)
		for _, want := range []string{"background", "color", "border-radius"} {
			if !strings.Contains(ss, want) {
				t.Errorf("StyleSheet(dark=%v) missing %q property", dark, want)
			}
		}
	}
}

func TestStyleSheetDiffersBetweenLightAndDark(t *testing.T) {
	if StyleSheet(false) == StyleSheet(true) {
		t.Fatal("light and dark stylesheets must differ")
	}
}

func TestTokensSizesMatchFyneOriginal(t *testing.T) {
	tok := Tokens{}
	if tok.TextSize() != 13 || tok.CaptionTextSize() != 11 || tok.HeadingTextSize() != 20 {
		t.Fatal("size tokens must match the values ported from the Fyne theme")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/uitheme/... -v`
Expected: FAIL — `BackgroundColor`/`StyleSheet`/`Tokens` not defined.

- [ ] **Step 3: Write the implementation**

Structure (fill in every hex value transcribed from the current
`uitheme.go` per the table above — the code below shows the shape and the
one token whose exact value is specified in this plan; every other token's
literal value comes from the implementer's transcription step):

```go
// Package uitheme owns OpenFortiTray's color and size tokens and renders
// them as a Qt stylesheet. The Background token is alpha-bearing on
// purpose: Qt's WA_TranslucentBackground needs the widget's own paint to
// leave native vibrancy (NSVisualEffectView / DWM Acrylic / X11 blur)
// showing through, the same role this token played as Fyne's GL clear
// color.
package uitheme

import "fmt"

// Tokens exposes size constants as methods so call sites read like
// tok.TextSize() rather than a bare package-level constant grab-bag.
type Tokens struct{}

func (Tokens) TextSize() float64        { return 13 }
func (Tokens) CaptionTextSize() float64 { return 11 }
func (Tokens) SubHeadingTextSize() float64 { return 15 }
func (Tokens) HeadingTextSize() float64 { return 20 }
func (Tokens) Padding() float64         { return 5 }
func (Tokens) InnerPadding() float64    { return 10 }
func (Tokens) CardRadius() float64      { return 8 }
func (Tokens) ButtonRadius() float64    { return 6 }
func (Tokens) InputRadius() float64     { return 6 }
func (Tokens) SeparatorThickness() float64 { return 1 }

type palette struct {
	background, headerBackground, menuBackground, overlayBackground string
	backgroundAlpha                                                 uint8
	foreground, placeholder, disabled, separator, inputBorder       string
	primary, foregroundOnPrimary, inputBackground, button           string
	success, warning, error_, hover                                 string
	hoverAlpha                                                      uint8
}

var light = palette{
	background:          "#F6F7F9",
	backgroundAlpha:      0x40,
	headerBackground:     "#FFFFFF",
	menuBackground:       "#FFFFFF",
	overlayBackground:    "#FFFFFF",
	foreground:           "#171A1F",
	placeholder:          "#5C6470",
	disabled:             "#5C6470",
	separator:            "#E2E5EA",
	inputBorder:          "#E2E5EA",
	primary:              "#2F6FEB",
	foregroundOnPrimary:  "#FFFFFF",
	inputBackground:      "#FFFFFF",
	button:               "#FFFFFF",
	success:              "#2E9E5B",
	warning:              "#B87514",
	error_:               "#C4362F",
	hover:                "#000000",
	hoverAlpha:           0x10,
}

var dark = palette{
	background:          "#16181C",
	backgroundAlpha:      0x40,
	headerBackground:     "#1E2128",
	menuBackground:       "#1E2128",
	overlayBackground:    "#1E2128",
	foreground:           "#EDEFF2",
	placeholder:          "#9AA2AE",
	disabled:             "#9AA2AE",
	separator:            "#2C313A",
	inputBorder:          "#2C313A",
	primary:              "#5B93F5",
	foregroundOnPrimary:  "#0E1013",
	inputBackground:      "#22262E",
	button:               "#22262E",
	success:              "#41BE77",
	warning:              "#E0A140",
	error_:               "#E86A62",
	hover:                "#FFFFFF",
	hoverAlpha:           0x12,
}

// BackgroundColor returns the alpha-bearing background token as separate
// RGBA components, for the glass-attach native code (Task 8) which needs
// the raw alpha value directly rather than a QSS string.
func BackgroundColor(dark bool) (r, g, b, a uint8) {
	p := paletteFor(dark)
	r, g, b = hexToRGB(p.background)
	return r, g, b, p.backgroundAlpha
}

func paletteFor(dark bool) palette {
	if dark {
		return darkPalette()
	}
	return lightPalette()
}

func lightPalette() palette { return light }
func darkPalette() palette  { return dark }

func hexToRGB(hex string) (r, g, b uint8) {
	var ri, gi, bi int
	fmt.Sscanf(hex, "#%02x%02x%02x", &ri, &gi, &bi)
	return uint8(ri), uint8(gi), uint8(bi)
}

// StyleSheet renders the full QSS the app applies once at startup via
// (*qt.QWidget).SetStyleSheet on the central widget — Qt propagates it to
// every descendant widget unless overridden locally.
func StyleSheet(dark bool) string {
	p := paletteFor(dark)
	t := Tokens{}
	return fmt.Sprintf(`
QWidget {
	background: rgba(%[1]s);
	color: %[2]s;
	font-size: %[3]vpx;
}
QPushButton {
	background: %[4]s;
	color: %[2]s;
	border-radius: %[5]vpx;
	padding: 6px 12px;
}
QPushButton:disabled {
	color: %[6]s;
}
QLineEdit, QComboBox {
	background: %[7]s;
	border: 1px solid %[8]s;
	border-radius: %[9]vpx;
	padding: 4px 6px;
}
QLabel[role="caption"] {
	color: %[6]s;
	font-size: %[10]vpx;
}
QLabel[role="success"] { color: %[11]s; }
QLabel[role="warning"] { color: %[12]s; }
QLabel[role="error"] { color: %[13]s; }
`,
		rgbaCSS(p.background, p.backgroundAlpha), // 1
		p.foreground,                             // 2
		t.TextSize(),                             // 3
		p.button,                                 // 4
		t.ButtonRadius(),                         // 5
		p.disabled,                               // 6
		p.inputBackground,                        // 7
		p.inputBorder,                             // 8
		t.InputRadius(),                          // 9
		t.CaptionTextSize(),                      // 10
		p.success,                                // 11
		p.warning,                                // 12
		p.error_,                                 // 13
	)
}

func rgbaCSS(hex string, alpha uint8) string {
	r, g, b := hexToRGB(hex)
	return fmt.Sprintf("%d,%d,%d,%d", r, g, b, alpha)
}
```

The implementer must extend the QSS
template with a rule for every remaining token that later tasks reference
(HeaderBackground, MenuBackground, OverlayBackground, Separator, Primary,
ForegroundOnPrimary, Hover) using the same `[role="..."]` selector-property
pattern shown above — Tasks 4-7's authors will grep this file for the exact
property/selector names to use, so leave a one-line comment next to each
rule naming which later widget it's for if it's not obvious from the name.

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/uitheme/... -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/uitheme/
git commit -m "feat(uitheme): rewrite as a Qt stylesheet token map"
```

---

### Task 4: `internal/shell` rewrite — QMainWindow nav rail + content stack

**Files:**
- Modify: `internal/shell/shell.go` (full rewrite)
- Test: `internal/shell/shell_test.go` (full rewrite — the current
  `render_capture_test.go`/`integration_capture_test.go` files under this
  package, if any, are Fyne-test-driver-based and get deleted in Task 11, not
  here; leave them in place and failing-to-compile is fine per Global
  Constraints until then — actually: since this task fully rewrites
  `shell.go`'s types, anything in this package that still imports Fyne will
  fail to compile as part of `go vet ./internal/shell/...`. Delete any
  Fyne-only test file in *this* package as part of *this* task instead of
  waiting for Task 11 — Task 11 only needs to handle the cross-package sweep.
  Check with `grep -l fyne.io internal/shell/*_test.go` and delete what it
  finds.)

**Interfaces:**
- Consumes: `uitheme.StyleSheet` (Task 3, for the rail's selected/unselected
  button styling).
- Produces (preserving the exact shape the current Fyne version has, per
  Global Constraints — only the widget types change):
```go
type Section int
const (
	SectionStatus Section = iota
	SectionConnection
	SectionAdvanced
)

type Parts struct {
	Status, Connection, Advanced *qt.QWidget
	ProfileBar, Banner, Footer   *qt.QWidget
}

type Shell struct {
	AttachGlass func(win *qt.QMainWindow)
}

func New(win *qt.QMainWindow, p Parts) *Shell
func (s *Shell) Select(sec Section)
func (s *Shell) Current() Section
func (s *Shell) Reveal(sec Section)
```

`RequestHeight`/`extraHeight` are dropped per the design doc and the
pre-plan research's explicit finding that they exist only to work around
Fyne having no size-to-content primitive. Task 5's activity-log disclosure
instead calls `(*qt.QWidget).AdjustSize()` on the window directly — Task 5's
author is told this in its own dispatch, not fixed here.

**Layout:** `QMainWindow` → `SetCentralWidget` a `QWidget` holding a
`QHBoxLayout` with contents-margins 0 and spacing 0: left child is a
fixed-width (150px, matching the approved mock) rail `QWidget` with a
`QVBoxLayout` of 3 checkable `QPushButton`s (Status/Connection/Advanced,
`SetCheckable(true)`, mutually exclusive via a `QButtonGroup`); right child
is a `QStackedWidget` with the three `Parts` widgets added via `AddWidget`
in Status, Connection, Advanced order (indices 0, 1, 2 — `Section`'s
`iota` order already matches this, so `stack.SetCurrentIndex(int(sec))`
is the entire switching logic — much simpler than the Fyne
Show/Hide-every-sibling approach, since `QStackedWidget` already only shows
one child).

- [ ] **Step 1: Write the failing test**

```go
package shell

import (
	"os"
	"testing"

	qt "github.com/mappu/miqt/qt6"
)

func newTestApp(t *testing.T) {
	t.Helper()
	// One QApplication per test binary run; miqt panics on a second
	// construction, so guard with a package-level sync.Once-style check.
	ensureQApplication()
}

func TestSelectSwitchesStackedWidgetIndex(t *testing.T) {
	newTestApp(t)
	win := qt.NewQMainWindow2()
	p := Parts{
		Status:     qt.NewQWidget(nil),
		Connection: qt.NewQWidget(nil),
		Advanced:   qt.NewQWidget(nil),
	}
	s := New(win, p)

	s.Select(SectionConnection)
	if s.Current() != SectionConnection {
		t.Fatalf("Current() = %v, want SectionConnection", s.Current())
	}

	s.Select(SectionAdvanced)
	if s.Current() != SectionAdvanced {
		t.Fatalf("Current() = %v, want SectionAdvanced", s.Current())
	}
}

func TestRevealSelectsAndShowsWindow(t *testing.T) {
	newTestApp(t)
	win := qt.NewQMainWindow2()
	p := Parts{
		Status:     qt.NewQWidget(nil),
		Connection: qt.NewQWidget(nil),
		Advanced:   qt.NewQWidget(nil),
	}
	s := New(win, p)
	glassCalled := false
	s.AttachGlass = func(w *qt.QMainWindow) { glassCalled = true }

	s.Reveal(SectionAdvanced)

	if s.Current() != SectionAdvanced {
		t.Fatal("Reveal must select the requested section")
	}
	if !glassCalled {
		t.Fatal("Reveal must call AttachGlass")
	}
	if !win.IsVisible() {
		t.Fatal("Reveal must show the window")
	}
}

func TestMain(m *testing.M) {
	qt.NewQApplication(os.Args)
	os.Exit(m.Run())
}
```

`ensureQApplication` in the non-test file is unnecessary once `TestMain`
constructs the single `QApplication` for the whole test binary — remove the
`newTestApp`/`ensureQApplication` helper and call sites, keeping only
`TestMain`. (Every later task's Qt-dependent test file follows this same
`TestMain` pattern — one `QApplication` per test binary, never per test.)

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/shell/... -v`
Expected: FAIL — current `shell.go` still has Fyne-typed `New`/`Parts`.

- [ ] **Step 3: Write the implementation**

```go
// Package shell hosts the app's single window: a fixed nav rail on the
// left (Status/Connection/Advanced) and a QStackedWidget content pane on
// the right holding all three sections' widgets simultaneously, matching
// the already-approved "one window, one level of navigation" design.
package shell

import qt "github.com/mappu/miqt/qt6"

type Section int

const (
	SectionStatus Section = iota
	SectionConnection
	SectionAdvanced
)

type Parts struct {
	Status, Connection, Advanced *qt.QWidget
	ProfileBar, Banner, Footer   *qt.QWidget
}

type Shell struct {
	AttachGlass func(win *qt.QMainWindow)

	win     *qt.QMainWindow
	stack   *qt.QStackedWidget
	navBtns [3]*qt.QPushButton
	current Section
}

const railWidth = 150

var navLabels = [3]string{"Status", "Connection", "Advanced"}

func New(win *qt.QMainWindow, p Parts) *Shell {
	s := &Shell{win: win}

	root := qt.NewQWidget(nil)
	rootLayout := qt.NewQHBoxLayout2()
	rootLayout.SetContentsMargins(0, 0, 0, 0)
	rootLayout.SetSpacing(0)

	rail := qt.NewQWidget(nil)
	rail.SetFixedWidth(railWidth)
	railLayout := qt.NewQVBoxLayout2()
	railLayout.SetContentsMargins(10, 18, 10, 18)
	railLayout.SetSpacing(4)

	group := qt.NewQButtonGroup2(rail)
	for i, label := range navLabels {
		btn := qt.NewQPushButton3(label)
		btn.SetCheckable(true)
		sec := Section(i)
		btn.OnPressed(func() { s.Select(sec) })
		group.AddButton(btn.QAbstractButton)
		railLayout.AddWidget(btn.QWidget)
		s.navBtns[i] = btn
	}
	railLayout.AddStretch()
	rail.SetLayout(railLayout.QLayout)

	s.stack = qt.NewQStackedWidget2()
	s.stack.AddWidget(p.Status)
	s.stack.AddWidget(p.Connection)
	s.stack.AddWidget(p.Advanced)

	rootLayout.AddWidget(rail)
	rootLayout.AddWidget(s.stack.QWidget)
	root.SetLayout(rootLayout.QLayout)

	win.SetCentralWidget(root)
	s.Select(SectionStatus)
	return s
}

// Select switches the visible content-pane section and updates the rail's
// selected-button styling.
func (s *Shell) Select(sec Section) {
	s.current = sec
	s.stack.SetCurrentIndex(int(sec))
	for i, btn := range s.navBtns {
		btn.SetChecked(Section(i) == sec)
	}
}

func (s *Shell) Current() Section { return s.current }

// Reveal selects sec, shows and raises the window, and re-attaches native
// vibrancy (idempotent — see Task 8 — safe to call on every reveal).
func (s *Shell) Reveal(sec Section) {
	s.Select(sec)
	s.win.Show()
	s.win.Raise()
	s.win.ActivateWindow()
	if s.AttachGlass != nil {
		s.AttachGlass(s.win)
	}
}
```

Verify `qt.NewQButtonGroup2` and `(*QButtonGroup).AddButton` exist with this
shape by grepping
`~/go/pkg/mod/github.com/mappu/miqt@v0.14.0/qt6/gen_qbuttongroup.go` before
relying on it; if the constructor or method differs, use whatever the real
generated signature is and note the discrepancy in the task report — the
mutual-exclusivity behavior (only one nav button checked at a time) is the
requirement, `QButtonGroup` is the expected mechanism but not sacred if the
real API shape differs slightly.

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/shell/... -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/shell/
git commit -m "feat(shell): rewrite nav rail + content pane on QMainWindow/QStackedWidget"
```

---

### Task 5: `internal/status` rewrite — connection state view

**Files:**
- Modify: `internal/status/status.go` (full rewrite)
- Test: `internal/status/status_test.go` (full rewrite; delete
  `render_capture_test.go` in this package now, same reasoning as Task 4)

**Interfaces:**
- Consumes: `uistate.ViewFor`, `uistate.View`, `uistate.Kind`,
  `uistate.NewRing`/`Ring`/`Entry` (all unchanged, `internal/uistate`),
  `uitheme.StyleSheet` (Task 3, `[role="..."]` properties for success/
  warning/error labels).
- Produces (preserving shape, Fyne types swapped for Qt):
```go
type Host interface {
	Connect()
	Disconnect()
	ShowSettings()
	OpenLog()
	GatewayLabel() string
	DTLSLabel() string
}

const WindowHeight = 600

type Controller struct{}

func New(host Host, win *qt.QMainWindow) *Controller
func (c *Controller) SetClock(now func() time.Time)
func (c *Controller) Content() *qt.QWidget
func (c *Controller) Apply(e tunnel.Event)
func (c *Controller) Tick()
```

`Show()` and `OnHeightRequest` are dropped: `Show()` was already a no-op in
the Fyne version (kept only for symmetry — confirm this in the current file
before deleting, per the pre-plan research), and `OnHeightRequest` is
replaced by calling `c.win.AdjustSize()` directly from inside the activity
toggle's own handler (Qt's layout system recomputes the window's size hint
from visible children automatically — no callback to the shell needed, per
Task 4's note).

**State → content mapping** (every value is from the pre-plan research —
transcribe, don't invent):

| State | Ring/dot color (`QLabel` or a small custom-painted `QWidget`) | State text | Sub text | Timer text | Primary button |
|---|---|---|---|---|---|
| Idle/Disconnected | Disabled (gray) | `v.Title` | `host.GatewayLabel()` or "no gateway configured" | (empty) | "Connect" → `host.Connect()` |
| Busy (`v.Busy()`) | Warning (yellow) | `v.Title` | `v.Detail` | (empty) | "Cancel" → `host.Disconnect()` |
| Connected (`CanDisconnect` && not busy) | Success (green) | `v.Title` | gateway label | `HH:MM:SS` uptime, ticked by `Tick()` | "Disconnect" → `host.Disconnect()` |
| Error (`Kind == KindBad`) | Error (red) | `v.Title` | `v.Detail` | (empty) | "Connect" → `host.Connect()` |

Details card (two-column key/value, shown only while connected): "Assigned
IP" → `v.AssignedIP`, "Protocol" → `"Fortinet · " + host.DTLSLabel()`,
"Connected since" → `HH:MM` of the moment `Connected` first became true
(reset to em-dash `—` in every other state, exactly as the Fyne version
does — grep `connectedAt` in the current file to confirm the exact reset
rule before transcribing).

Activity log: `QToolButton` (arrow icon changes via `setArrowType`,
`Qt::DownArrow`/`Qt::RightArrow`) labeled `"Activity (%d)"` toggles a
`QScrollArea` containing one row per `uistate.Entry` (newest first, capped
at 50 by the existing `Ring`), each row a `QLabel` with text
`"<timestamp> — <text>"` using the same `15:04:05` / `2 Jan 15:04` format
rule as the Fyne version (today vs. not-today) — transcribe the exact
`time.Format` layout strings and the "is it today" comparison from the
current file rather than re-deriving them.

- [ ] **Step 1: Write the failing test**

```go
package status

import (
	"os"
	"testing"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"openfortitray/internal/tunnel" // use the module's actual import path — grep go.mod's module line to confirm
)

type fakeHost struct {
	connected, disconnected bool
}

func (f *fakeHost) Connect()             { f.connected = true }
func (f *fakeHost) Disconnect()          { f.disconnected = true }
func (f *fakeHost) ShowSettings()        {}
func (f *fakeHost) OpenLog()             {}
func (f *fakeHost) GatewayLabel() string { return "vpn.example.com" }
func (f *fakeHost) DTLSLabel() string    { return "DTLS on" }

func TestMain(m *testing.M) {
	qt.NewQApplication(os.Args)
	os.Exit(m.Run())
}

func TestContentReturnsNonNilWidget(t *testing.T) {
	win := qt.NewQMainWindow2()
	c := New(&fakeHost{}, win)
	if c.Content() == nil {
		t.Fatal("Content() returned nil")
	}
}

func TestApplyConnectedEnablesDisconnectButton(t *testing.T) {
	win := qt.NewQMainWindow2()
	host := &fakeHost{}
	c := New(host, win)
	c.Apply(tunnel.Event{State: tunnel.StateConnected /* fill in whatever fields tunnel.Event actually needs — grep internal/tunnel/tunnel.go for the real struct shape before writing this test */})
	// Behavioral assertion: after a Connected event, clicking the primary
	// action must call Disconnect, not Connect. Exercise this through
	// whatever exported hook exists (e.g. simulate a click via the
	// button found by object name, or add a small test-only accessor) —
	// the concrete mechanism depends on how Task 5's author structures
	// the primary-button field; the requirement is this behavior, not
	// this exact test code.
}
```

The implementer must replace the `tunnel.Event{...}` literal and the click
simulation with real field names and a real way to trigger the primary
button's click from a test — `Read internal/tunnel/tunnel.go` for the
`Event` struct shape and `Read` the current (pre-rewrite) `status_test.go`
if it has any surviving non-Fyne-driver tests for a pattern to follow. This
test skeleton establishes the two behaviors worth covering
(non-nil content; state-driven button wiring) — flesh out the exact
assertions once those two files are read.

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/status/... -v`
Expected: FAIL — current file is still Fyne-typed.

- [ ] **Step 3: Write the implementation**

Build `Content()`'s widget tree top-to-bottom: a `QVBoxLayout` with (1) the
hero block (ring `QWidget`/`QLabel` + state `QLabel` + sub `QLabel` + timer
`QLabel`, all center-aligned, matching the mock), (2) the primary
`QPushButton`, (3) the details card (`QWidget` with a `QFormLayout` of the
three key/value rows, `SetProperty("role", "success")`-style QSS hooks
where the mapping table calls for a themed color), (4) the activity
`QToolButton` + `QScrollArea` (initially hidden via `SetVisible(false)`).
Store every widget that `Apply`/`Tick` need to mutate as unexported
`Controller` fields (mirrors the current Fyne version's own field list —
`Read` it once more during implementation to carry over every field name
so `Apply`'s logic transcribes cleanly). `Apply(e)` calls
`uistate.ViewFor(e)` exactly once, feeds the ring into `c.ring.Add(e, ...)`
(reuse `SetClock`'s injected `now` the same way the current file does for
testability), then sets every widget from the state→content table above —
no branching logic beyond what the table specifies. `Tick()` only recomputes
the uptime `QLabel` text when `v.State == tunnel.StateConnected` (or
whatever the exact current condition is — transcribe it, don't guess).

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/status/... -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/status/
git commit -m "feat(status): rewrite connection state view on Qt widgets"
```

---

### Task 6: `internal/tray` rewrite — QSystemTrayIcon + QMenu

**Files:**
- Modify: `internal/tray/tray.go` (full rewrite)
- Keep unchanged: `internal/tray/badge.go` (pure stdlib `image` code, zero
  Fyne dependency — verify with `grep fyne internal/tray/badge.go` before
  touching; expected to print nothing)
- Keep unchanged: `internal/tray/icons.go` (embedded PNG bytes, zero Fyne
  dependency — same verification)
- Test: `internal/tray/tray_test.go` (full rewrite)

**Interfaces:**
- Consumes: `badge.Composite(...)` (or whatever its real exported name is —
  grep `internal/tray/badge.go` for its signature before writing code
  against it) returning PNG bytes; `icons.go`'s 4 embedded byte slices.
- Produces (preserving shape):
```go
type App interface {
	Connect()
	Disconnect()
	SetAutostart(on bool) error
	AutostartEnabled() bool
	LogPath() string
	Version() string
	ShowSettings()
	ShowStatus()
	Quit()
	UpdateClicked()
}

type Controller struct{}

func Setup(app App) (*Controller, error)
func SetTooltip(text string)
func (c *Controller) Apply(e tunnel.Event)
func (c *Controller) SetUpdateAvailable(version string)
func (c *Controller) ReassertTray()
```

`Setup` drops its `fyne.App` first parameter (Qt's `QSystemTrayIcon` needs no
app-driver handle, only a `QApplication` already constructed — which Task 10
guarantees by construction order). `SetTooltip` becomes a direct
`(*qt.QSystemTrayIcon).SetToolTip` call on the controller's own icon instead
of reaching into `fyne.io/systray`'s package-level singleton — this drops the
whole `recover()`-guarded workaround the pre-plan research flagged, since Qt
gives a real, always-available API for this. `ReassertTray` is kept as a
method (Task 10 may still call it at the same lifecycle points) but its body
becomes `c.icon.Show()` — cheap and idempotent — rather than a full
teardown/rebuild, since Qt's tray icon has no Windows pre-`app.Run()` timing
gap to work around; note in the task report whether call sites in Task 10
turn out to need it at all.

**Icon state → QIcon:** on `Setup`, decode each of the 4 embedded PNGs via
`qt.NewQPixmap()` + `(*QPixmap).LoadFromDataWithData(pngBytes)`, wrap each in
`qt.NewQIcon2(pixmap)`, and additionally run each through the existing
`badge.go` compositor to precompute the 4 badged variants exactly as the
current file does — same 8 total `QIcon`s cached as `Controller` fields.
Track the current state directly as a `uistate.Kind` field (drop the
`resourceFor`/`currentIcon []byte` byte-comparison indirection entirely —
Qt's `QIcon` isn't opaque the way Fyne's wrapped resource was, and the
`Controller` already owns every `QIcon` it could possibly set, so it can
just remember which one by name).

**Menu structure** (identical order/behavior to the current file — see the
table the pre-plan research produced):

```go
menu := qt.NewQMenu2()
titleAction := menu.AddActionWithText(fmt.Sprintf("OpenFortiTray %s", app.Version()))
titleAction.SetEnabled(false)
menu.AddSeparator()
statusAction := menu.AddActionWithText("")   // text set per-state by Apply
statusAction.SetEnabled(false)
actionItem := menu.AddActionWithText("Connect") // label/handler rebound per-state by setAction, see below
menu.AddSeparator()
openAction := menu.AddActionWithText("Open")
openAction.OnTriggered(app.ShowStatus)
settingsAction := menu.AddActionWithText("Settings…")
settingsAction.OnTriggered(app.ShowSettings)
menu.AddSeparator()
autostartAction := menu.AddActionWithText("Auto-connect at login")
autostartAction.SetCheckable(true)
autostartAction.SetChecked(app.AutostartEnabled())
autostartAction.OnTriggered(func() { toggleAutostart(app, autostartAction) })
logsAction := menu.AddActionWithText("View logs")
logsAction.OnTriggered(func() { xopen.File(app.LogPath()) })
updateAction := menu.AddActionWithText("Check for Updates…") // label flips via SetUpdateAvailable
updateAction.OnTriggered(app.UpdateClicked)
menu.AddSeparator()
quitAction := menu.AddActionWithText("Quit")
quitAction.OnTriggered(app.Quit)
```

`setAction` (rebinding `actionItem`'s label + click target between
Connect/Cancel/Disconnect from the same `uistate.View` the status/settings
controllers use) must be re-implemented against `QAction`: since `OnTriggered`
can only be registered once and stacks handlers rather than replacing them if
called repeatedly, store the *current* target function in a `Controller`
field and register `actionItem.OnTriggered(func() { c.currentAction() })`
exactly once in `Setup` — `Apply` then only reassigns `c.currentAction` and
`actionItem.SetText(...)`, never calls `OnTriggered` again. Apply this same
once-registered-indirect-dispatch pattern anywhere else a handler's target
needs to change after `Setup` (there shouldn't be another case, but note it
if one turns up).

- [ ] **Step 1: Write the failing test**

```go
package tray

import (
	"os"
	"testing"

	qt "github.com/mappu/miqt/qt6"
)

type fakeApp struct {
	connected, disconnected, quit bool
	autostart                     bool
}

func (f *fakeApp) Connect()               { f.connected = true }
func (f *fakeApp) Disconnect()            { f.disconnected = true }
func (f *fakeApp) SetAutostart(on bool) error { f.autostart = on; return nil }
func (f *fakeApp) AutostartEnabled() bool { return f.autostart }
func (f *fakeApp) LogPath() string        { return "/tmp/log" }
func (f *fakeApp) Version() string        { return "0.0.0-test" }
func (f *fakeApp) ShowSettings()          {}
func (f *fakeApp) ShowStatus()            {}
func (f *fakeApp) Quit()                  { f.quit = true }
func (f *fakeApp) UpdateClicked()         {}

func TestMain(m *testing.M) {
	qt.NewQApplication(os.Args)
	os.Exit(m.Run())
}

func TestSetupSucceeds(t *testing.T) {
	c, err := Setup(&fakeApp{})
	if err != nil {
		t.Fatalf("Setup returned error: %v", err)
	}
	if c == nil {
		t.Fatal("Setup returned nil controller")
	}
}

func TestSetUpdateAvailableChangesMenuLabel(t *testing.T) {
	c, err := Setup(&fakeApp{})
	if err != nil {
		t.Fatal(err)
	}
	c.SetUpdateAvailable("v9.9.9")
	// Assert the update action's text changed — access it via whatever
	// exported or test-visible path the implementation provides. If
	// Controller keeps updateAction unexported with no accessor, add a
	// small unexported-field test in this same package (same-package
	// tests can read unexported fields directly) rather than exporting
	// internals just for the test.
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/tray/... -v`
Expected: FAIL.

- [ ] **Step 3: Write the implementation**

Follow the menu-structure and icon-caching code shown above; wire `Apply(e)`
to call `uistate.ViewFor(e)`, pick the cached badged/unbadged `QIcon` by
`v.Kind`, call `c.icon.SetIcon(...)`, `statusAction.SetText(v.MenuLabel)` (or
whatever field the current file actually reads for the status line — grep
it), and `setAction`'s reassignment described above.

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/tray/... -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tray/
git commit -m "feat(tray): rewrite on QSystemTrayIcon/QMenu"
```

---

### Task 7: `internal/settings` render-layer rewrite

**Files:**
- Modify: `internal/settings/settings.go` (full rewrite)
- Do not touch: `internal/settings/logic.go` (per Global Constraints)
- Test: `internal/settings/settings_test.go` (full rewrite; delete
  `render_capture_test.go` in this package now, same reasoning as Task 4)

**Interfaces:**
- Consumes: every exported `logic.go` function/type listed in the pre-plan
  research verbatim (validators, `Issue`, tab/field constants, label⇄enum
  round-trips) — none of these change shape.
- Produces (preserving shape):
```go
type Host interface {
	Config() *config.Config
	Commit(c *config.Config) error
	Connect()
	Disconnect()
}

type Controller struct{}

func New(host Host, win *qt.QMainWindow) *Controller
func (c *Controller) Apply(e tunnel.Event)
func (c *Controller) Banner() *qt.QWidget
func (c *Controller) ProfileBar() *qt.QWidget
func (c *Controller) ConnectionContent() *qt.QWidget
func (c *Controller) AdvancedContent() *qt.QWidget
func (c *Controller) Footer() *qt.QWidget
func (c *Controller) SetNavigator(nav func(tab string))
func (c *Controller) ShowIssue(issue *Issue)
```

`Issue` itself is defined in `logic.go` and unchanged.

**Field → widget → logic.go call mapping** (exhaustive — every row must be
implemented; this is the task's actual bulk of work). Build each row as:
construct the Qt widget, wire its change signal to call the named `logic.go`
function, and on a validation error call the shared `markInvalid(widget,
err)` helper (written once, used by every row — see below) instead of a
per-row bespoke error path.

| Field | Widget | Change signal → logic.go call |
|---|---|---|
| Profile name | `QLineEdit` | `OnTextChanged` → `validateName` then `renameProfile` on blur/Save |
| Gateway | `QLineEdit` | `OnTextChanged` → `validateHost` |
| Port | `QLineEdit` | `OnTextChanged` → `validatePortString`/`parsePort`, display via `effectivePort` |
| Protocol (SSL/IPsec) | `QComboBox` (items = a fixed 2-entry list, "SSL VPN"/"IPsec (IKEv2)") | `OnCurrentIndexChanged` → `backendFromLabel` |
| IPsec auth (PSK/Cert) | `QComboBox` (`ipsecAuthLabels`) | `OnCurrentIndexChanged` → `ipsecAuthFromLabel`; drives visibility of the PSK vs Cert/Key rows below via plain `SetVisible` (no relayout workaround needed — see Global Constraints) |
| PSK secret | `QLineEdit` (`SetEchoMode(qt.QLineEdit__Password)`) | `OnTextChanged` → written to the per-profile dirty/value maps exactly as the current file does; persisted at Save via `credstore.Set(config.IPsecPSKCredstoreKey(...))` |
| Cert path | `QLineEdit` (read-only) + browse `QPushButton` | button → `QFileDialog` (see below) |
| Key path | `QLineEdit` (read-only) + browse `QPushButton` | same as Cert path |
| Auth method | `QComboBox` (`authLabels`) | `OnCurrentIndexChanged` → `authMethod`; Save/Connect gate via `validateAuthSupported` |
| Auto-connect at login | `QCheckBox` | `OnToggled` → direct bool write |
| Keep VPN up | `QCheckBox` | `OnToggled` → direct bool write |
| IPv6 | `QCheckBox` | `OnToggled` → direct bool write |
| DTLS | `QCheckBox` | `OnToggled` → direct bool write |
| Remember session | `QCheckBox` | `OnToggled` → direct bool write |
| Server-cert mode (Warn/Pin) | `QComboBox` (`certModeLabels`) | `OnCurrentIndexChanged` → `certMode`; drives Fingerprint row visibility |
| Fingerprint | `QLineEdit` | `OnTextChanged` → `fingerprintCharset` (live); Save uses `validateFingerprint` |
| Split-DNS | `QPlainTextEdit` (multiline) | `OnTextChanged` (or the nearest signal `QPlainTextEdit` exposes — grep for it) → `validateSplitDNSText`/`parseSplitDNS` |
| SAML port | `QLineEdit` | `OnTextChanged` → `validatePortString`, display via `effectiveSAMLPort` |
| openconnect path | `QLineEdit` | `OnTextChanged` → `openconnectPathEntryValidator`; Save uses `validateOpenconnectPath` |
| Helper path | `QLabel` (read-only display) | none |
| IKE proposal | `QLineEdit` | direct write, no validator (inert for SSL profiles) |
| ESP proposal | `QLineEdit` | direct write, no validator |
| Local ID | `QLineEdit` | direct write, no validator |
| Remote ID | `QLineEdit` | direct write, no validator |

**Shared error-display helper** (write once, use for every validated row —
replaces Fyne's `SetValidationError`/`AlwaysShowValidationError`):

```go
func markInvalid(w *qt.QLineEdit, errLabel *qt.QLabel, err error) {
	if err != nil {
		w.SetStyleSheet(`border: 1px solid ` + errorBorderColor + `;`)
		errLabel.SetText(err.Error())
		errLabel.SetVisible(true)
	} else {
		w.SetStyleSheet("")
		errLabel.SetVisible(false)
	}
}
```

Every `QLineEdit` row that has a validator gets one small `QLabel` directly
below it in the row's own `QVBoxLayout`, hidden by default, shown/populated
only by `markInvalid`. `errorBorderColor` comes from `uitheme` (Task 3) —
grep its `Error` token's QSS-ready color string rather than hardcoding a hex
here.

**Cert/Key file picker:**

```go
dlg := qt.NewQFileDialog4(c.win.QWidget, "Select client certificate")
dlg.SetFileMode(qt.QFileDialog__ExistingFile)
if dlg.Exec() == int(qt.QDialog__Accepted) {
	files := dlg.SelectedFiles()
	if len(files) > 0 {
		certPathEdit.SetText(files[0])
	}
}
```

Verify `QFileDialog.SetFileMode`/`.Exec`/`.SelectedFiles` and
`QDialog__Accepted`'s exact names against
`gen_qfiledialog.go`/`gen_qdialog.go` before relying on this signature
verbatim — the constructor variants were confirmed during planning
(`NewQFileDialog4(parent, caption)`), the rest were not individually grepped
and must be checked at implementation time.

**Delete-profile confirm** (`dialog.ShowConfirm` in the Fyne version):

```go
mb := qt.NewQMessageBox3(qt.QMessageBox__Question, "Delete Profile", "Delete this profile? This cannot be undone.")
mb.SetStandardButtons(qt.QMessageBox__Yes | qt.QMessageBox__No)
if mb.Exec() == int(qt.QMessageBox__Yes) {
	deleteProfile(sel)
}
```

Verify `SetStandardButtons`/`QMessageBox__Yes`/`QMessageBox__No` exact names
against `gen_qmessagebox.go` at implementation time the same way.

**Row show/hide:** every conditional row (IPsec-auth-dependent PSK/Cert/Key,
cert-mode-dependent Fingerprint, auth-method note) uses plain
`row.SetVisible(bool)` on the row's own container widget — no
`row()/show()/relayout()` port. Confirm during implementation that the
containing `QVBoxLayout` actually reflows on `SetVisible(false)` with a
quick manual run (per the `run` skill) rather than assuming; if it doesn't
reflow (some Qt layouts need `layout.Activate()` after a visibility change),
call that explicitly rather than reintroducing a hand-rolled relayout.

- [ ] **Step 1: Write the failing tests**

```go
package settings

import (
	"os"
	"testing"

	qt "github.com/mappu/miqt/qt6"

	"openfortitray/internal/config" // confirm real import path from go.mod
)

type fakeHost struct {
	cfg *config.Config
}

func (f *fakeHost) Config() *config.Config          { return f.cfg }
func (f *fakeHost) Commit(c *config.Config) error   { f.cfg = c; return nil }
func (f *fakeHost) Connect()                        {}
func (f *fakeHost) Disconnect()                     {}

func TestMain(m *testing.M) {
	qt.NewQApplication(os.Args)
	os.Exit(m.Run())
}

func TestContentPanesAreNonNil(t *testing.T) {
	win := qt.NewQMainWindow2()
	host := &fakeHost{cfg: &config.Config{}} // fill in whatever minimal valid Config the real type needs — grep config.go's zero-value expectations
	c := New(host, win)
	if c.ConnectionContent() == nil || c.AdvancedContent() == nil || c.Banner() == nil || c.ProfileBar() == nil || c.Footer() == nil {
		t.Fatal("every content pane must be non-nil")
	}
}

func TestShowIssueSwitchesToTheRightTabAndField(t *testing.T) {
	win := qt.NewQMainWindow2()
	host := &fakeHost{cfg: &config.Config{}}
	c := New(host, win)
	var navigated string
	c.SetNavigator(func(tab string) { navigated = tab })

	c.ShowIssue(&Issue{Tab: TabAdvanced, Field: FieldSplitDNS, Message: "bad domain"})

	if navigated != TabAdvanced {
		t.Fatalf("navigated = %q, want %q", navigated, TabAdvanced)
	}
	// Further assertion: the Split-DNS field's error label is now visible
	// and shows "bad domain" — access via an unexported field (same-package
	// test) once the implementation names it.
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/settings/... -v`
Expected: FAIL.

- [ ] **Step 3: Write the implementation**

Build `ConnectionContent()`/`AdvancedContent()` as `QWidget`s each holding a
`QFormLayout` (label + field per row) constructed from the field-mapping
table above, in the same visual order as the current Fyne version (grep it
once for ordering, since the table above doesn't re-specify row order within
each tab). `Banner()` is a `QLabel` (initially hidden) used by `ShowIssue`
for the persistent top message. `ProfileBar()` is a `QComboBox` (profile
picker) + add/duplicate/delete `QPushButton`s in a `QHBoxLayout`. `Footer()`
is the Save/Cancel/Reconnect `QPushButton` row. `Apply(e)` updates the
status strip text/color and the reconnect button's enabled state exactly as
the current file does (grep the exact condition). `ShowIssue` calls
`c.nav(issue.Tab)`, then locates the matching field's widget+error-label pair
(a small `map[string]fieldWidgets` keyed by the `Field*` constants, built
once in `New`, is the natural structure) and calls `markInvalid` on it with
`errors.New(issue.Message)`, then focuses the widget
(`(*qt.QLineEdit).SetFocus()`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./internal/settings/... -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/settings/
git commit -m "feat(settings): rewrite render layer on Qt widgets, logic.go unchanged"
```

---

### Task 8: Native vibrancy — swap handle source to Qt's `WinId()`

**Files:**
- Modify: `cmd/openfortitray/glass.go` (contract signature)
- Modify: `cmd/openfortitray/glass_other.go`
- Modify: `cmd/openfortitray/glass_darwin.go`, `glass_darwin.h`, `glass_darwin.m`
- Modify: `cmd/openfortitray/glass_windows.go`
- Modify: `cmd/openfortitray/glass_linux.go`, `glass_linux.h`
- Test: `cmd/openfortitray/glass_test.go` if one exists (check first); if
  none exists, none is required — this is OS-native glue that the `run`
  skill's manual verification (Task 12) covers, matching how it was verified
  originally.

**Interfaces:**
- Consumes: `uitheme.BackgroundColor(dark bool)` (Task 3) — darwin's
  `NSVisualEffectView` attach no longer needs it (material handles its own
  fill), but the Qt-side caller (Task 10) uses it to set
  `centralWidget.SetStyleSheet("background: rgba(...)")` matching Task 3's
  token, and `SetAttribute(qt.WA_TranslucentBackground, true)` — both calls
  belong in Task 10's window construction, not here; this task only fixes
  the native-handle plumbing.
- Produces: `attachGlass(w *qt.QWidget)` (was `attachGlass(w fyne.Window)`).

**glass.go contract change:**

```go
package main

import qt "github.com/mappu/miqt/qt6"

// attachGlass attaches native blur/acrylic behind w. Must be called after
// w's underlying native window exists (i.e. after Show()) — WinId() can
// return an invalid handle before then. Safe to call repeatedly
// (idempotent per platform — see glass_darwin.m's identifier-tag guard).
func attachGlass(w *qt.QWidget) {
	attachNativeGlass(uintptr(w.WinId()))
}
```

`glass_other.go` (non-darwin/windows/linux):

```go
//go:build !darwin && !windows && !linux

package main

func attachNativeGlass(nativeHandle uintptr) {}
```

**glass_darwin.go/.h/.m** — this is almost exactly the pre-plan spike's
`glass_qt_darwin.m`, already proven working; port it in as the real
implementation rather than rewriting from scratch:

`glass_darwin.h`:
```c
#include <stdint.h>
void oft_attach_glass(uintptr_t nsviewPtr);
```

`glass_darwin.go`:
```go
//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include "glass_darwin.h"
*/
import "C"

func attachNativeGlass(nativeHandle uintptr) {
	C.oft_attach_glass(C.uintptr_t(nativeHandle))
}
```

`glass_darwin.m`: copy the spike's `glass_qt_darwin.m` body verbatim (the
NSView→NSWindow hop via `.window`, the `NSVisualEffectView` wrap-as-sibling
logic, material `NSVisualEffectMaterialMenu` to match the existing Fyne
version's exact material choice — the spike used `NSVisualEffectMaterialSidebar`
for its own throwaway mock; **use `NSVisualEffectMaterialMenu` here** to
match the production Fyne implementation's already-approved look, not the
spike's placeholder), but add back the idempotence guard the current
(Fyne-based) `glass_darwin.m` has (tagged `NSVisualEffectView` identifier
check) — the spike didn't need it since it only ran once, but `Reveal()`
calls `attachGlass` on every window show, so it's required here. `Read` the
current `glass_darwin.m` once during implementation to copy that guard's
exact logic rather than re-deriving it.

**glass_windows.go:** keep the existing `DwmSetWindowAttribute` /
`DWMSBT_TRANSIENTWINDOW` logic completely unchanged — only replace how the
`HWND` value is obtained. Change:

```go
func attachNativeGlass(hwnd uintptr) {
	// ... existing DwmSetWindowAttribute body, unchanged, using hwnd directly
	// instead of extracting it from a driver.WindowsWindowContext.
}
```

**glass_linux.go/.h:** keep the existing `_KDE_NET_WM_BLUR_BEHIND_REGION`
X11 logic and the Wayland no-op branch completely unchanged — only replace
how the X11 `Window` handle is obtained (it was `driver.X11WindowContext`'s
field; it becomes the `nativeHandle uintptr` parameter directly, cast to
whatever X11 `Window` type the existing `oft_attach_x11_blur` C function
expects). Confirm at implementation time whether Qt's own platform
detection (X11 vs Wayland) needs querying differently than Fyne's
`driver.X11WindowContext` vs `driver.WaylandWindowContext` type-switch did —
`qt.QGuiApplication_PlatformName()` (verify this exact function name against
`gen_qguiapplication.go`) returns `"xcb"` or `"wayland"` and replaces the
type-switch.

- [ ] **Step 1: Read every current glass_*.go/.m/.h file in full** before
  editing, to carry over the exact DWM/X11/idempotence logic without
  re-deriving it from the pre-plan research summary alone.

- [ ] **Step 2: Make the edits described above**

- [ ] **Step 3: Verify each platform's file still compiles for its own GOOS**

Run (on macOS, the only platform available this session):
`CGO_CXXFLAGS=-std=c++17 GOOS=darwin go build ./cmd/openfortitray/... 2>&1 | grep glass`
Expected: no glass-related errors (other `cmd/openfortitray` errors from
not-yet-rewired `main.go` are expected and fine per Global Constraints).

Cross-compile checks for the other two platforms (type-checking only, no
linking, since cgo can't cross-compile — this at least catches obvious
syntax/type errors in the Go-side files, not the cgo bodies):
`CGO_ENABLED=0 GOOS=windows go vet ./cmd/openfortitray/... 2>&1 | grep glass_windows` —
expect this to fail cleanly with a cgo-disabled message about the file being
skipped, not a syntax error; if it reports a real syntax error in
`glass_windows.go`, fix it. Full verification for Windows/Linux happens via
CI (Task 2's setup) on push.

- [ ] **Step 4: Commit**

```bash
git add cmd/openfortitray/glass*.go cmd/openfortitray/glass*.h cmd/openfortitray/glass*.m
git commit -m "feat(glass): swap native-handle source from Fyne driver to Qt WinId()"
```

---

### Task 9: `updateflow.go` rewrite — QDialog-based update prompts

**Files:**
- Modify: `cmd/openfortitray/updateflow.go` (full rewrite)
- Test: `cmd/openfortitray/update_prompt_test.go` (check current content
  first — rewrite whatever of it is Fyne-widget-dependent; keep any part
  that's pure logic, e.g. testing `update.*` package calls with no widget
  construction)

**Interfaces:**
- Consumes: every `internal/update` function the current file calls
  (`update.Apply`, whatever check/download functions exist — grep the
  current file's `update.` call sites and keep them identical), Task 1's
  `uidispatch.Queue` (replaces every `fyne.Do` call site in this file).
- Produces: the same state-machine entry points the current file exposes to
  `main.go` (`showUpdatePrompt`/`prepare`/`finishUpdate` or whatever the
  actual current exported/unexported names are — grep first, preserve
  names so Task 10's rewiring is mechanical).

**States** (same 4 as the current file — offered/preparing/ready/failed):
build each as content set into a single reused `QDialog` via
`dlg.SetLayout(...)` swapped per state (mirrors the current file's "one
window, swap content" pattern), rather than 4 separate dialogs:
- Offered: message `QLabel` + Later/Download `QPushButton`s.
- Preparing: message `QLabel` + an indeterminate progress indicator
  (`QProgressBar` with `SetRange(0, 0)`, Qt's documented way to get
  `ProgressBarInfinite`-equivalent behavior).
- Ready: message `QLabel` + Later/"Restart now" `QPushButton`s.
- Failed: message `QLabel` + Close/"Open releases" `QPushButton`s.

Close behavior: override the dialog's close event to hide rather than
destroy (`QDialog`'s default `closeEvent` already just hides for a non-modal
dialog shown via `Show()` rather than `Exec()` — use `Show()`, not `Exec()`,
matching the current file's non-modal, non-blocking window per the pre-plan
research's note that no `dialog.*` package is used today).

- [ ] **Step 1: Read the current `updateflow.go` and `update_prompt_test.go`
  in full**, transcribing every exact string (button labels, message
  templates) and every `update.*` call site before writing new code — this
  file's text is user-facing copy that must not silently change during a
  framework swap.

- [ ] **Step 2: Write/adapt the failing tests** for whatever pure-logic
  behavior the current test file covers (state transitions, what
  `update.Apply` is called with) — skip anything that was asserting on Fyne
  widget structure specifically.

- [ ] **Step 3: Run tests to verify they fail**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./cmd/openfortitray/... -run TestUpdate -v`
Expected: FAIL (old file still Fyne-typed) or build error.

- [ ] **Step 4: Write the implementation** per the states above, replacing
  every `fyne.Do(...)` with `dispatchQueue.Post(...)` (the queue instance is
  a field Task 10 wires in — this task's functions take a
  `*uidispatch.Queue` parameter or method receiver rather than assuming a
  package-level global, matching how `main.go`'s `app` struct already holds
  everything else it needs).

- [ ] **Step 5: Run tests to verify they pass**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./cmd/openfortitray/... -run TestUpdate -race -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/openfortitray/updateflow.go cmd/openfortitray/update_prompt_test.go
git commit -m "feat(updateflow): rewrite update dialogs on QDialog"
```

---

### Task 10: `main.go` rewrite — QApplication bootstrap, dispatch wiring, watchdog removal

**Files:**
- Modify: `cmd/openfortitray/main.go` (the central rewiring task)
- Delete: `cmd/openfortitray/relaunch_unix.go`, `cmd/openfortitray/relaunch_windows.go`
  (confirmed sole caller was `recoverFromFreeze`, which this task deletes)
- Modify: `cmd/openfortitray/wake_darwin.go`, `wake_linux.go`, `wake_other.go`,
  `wake_windows.go` only if their function signatures currently take a Fyne
  type (per the pre-plan research they're plain `func(fn func())` — likely
  no change needed; confirm by reading them, don't assume)
- Test: `cmd/openfortitray/main_test.go` (substantial rewrite)

This is the task that makes `go build ./...` succeed again — everything
before this point built its own package only, per Global Constraints.

**Bootstrap sequence** (replacing the 20-step sequence the pre-plan research
documented, in the same order, dropping only what's noted):

1. *(unchanged)* Windows `GALLIUM_DRIVER` env hack — **drop it**. Ruling:
   this existed specifically for Fyne's Mesa/OpenGL software-rendering path
   on GPU-less Windows Cloud PCs; Qt's software rendering path (if needed at
   all) is a different mechanism. Record in the ledger: "dropped
   GALLIUM_DRIVER hack, no Qt equivalent verified — if a future report
   surfaces a GPU-less Windows RDP crash, that's the first place to look."
2. *(unchanged)* config load, log file, stderr redirect, single-instance
   flock, `events` chan, `app` struct construction.
3. `a.dispatch = uidispatch.New()` (new field, Task 1).
4. `authFn`, `runFn`, `a.sup = tunnel.New(...)` — unchanged.
5. `qtApp := newQApplication(os.Args)` (Task 2) — replaces
   `fyneapp.NewWithID`/`SetMetadata`/`sanitizeFynePreferences`. App
   metadata (name, ID) becomes
   `qtApp.SetApplicationName("OpenFortiTray")` /
   `qtApp.SetOrganizationName(...)` — verify exact `QCoreApplication`
   setter names against `gen_qcoreapplication.go` at implementation time.
6. Apply the theme: `qt.QApplication_SetStyleSheet(uitheme.StyleSheet(dark))`
   — `dark` from `qt.QGuiApplication_StyleHints()`'s color-scheme query if
   miqt exposes it, otherwise default `false` and note the gap in the task
   report rather than guessing an API that may not exist in this binding
   version.
7. `a.tray, err = tray.Setup(a)` (Task 6's new signature, no `fyneApp` arg).
8. `tray.SetTooltip("OpenFortiTray")`.
9. `win := qt.NewQMainWindow2()`, `a.win = win`. `win.SetAttribute(qt.WA_TranslucentBackground, true)`, central widget's stylesheet background set from `uitheme.BackgroundColor` (Task 3/8's note) — this is the direct replacement for `glfw.WindowHint(TransparentFramebuffer, true)` + the old theme's alpha background.
10. `a.settings = settings.New(a, win)` (Task 7), `a.status = status.New(a, win)` (Task 5).
11. `a.shell = shell.New(win, shell.Parts{...})` (Task 4); `a.shell.AttachGlass = attachGlass` (Task 8); `a.settings.SetNavigator(...)`.
12. `a.onConnectIssue = func(i) { a.settings.ShowIssue(i) }`; `a.installBootstrapHooks()` — unchanged.
13. `go a.pump()` — body changes from `fyne.Do(...)` to
    `a.dispatch.Post(...)` at its one call site.
14. `signal.Notify` + `go a.watchSignals(sigs, func(){ a.dispatch.Post(func(){ qt.QCoreApplication_Quit() }) })`.
15. `watchSystemSleep(a.onSystemWake)`, `watchScreenWake(a.onScreenWake)` —
    kept, bodies changed from `fyne.DoAndWait` to `a.dispatch.PostAndWait`.
    **`go a.watchMainThreadFreeze()` is deleted, not ported** — this is the
    core point of the migration; the `uidispatch`+`QTimer` design removes
    the freeze class rather than detecting it (per the design doc).
16. `consumeResumeMarker`, `go a.selfHealThenConnect(...)` — unchanged except
    its `fyne.Do(a.Connect)` closure becomes `a.dispatch.Post(a.Connect)`.
17. `go a.startUpdateChecker(...)` — unchanged except internal `fyne.Do` call
    sites become `a.dispatch.Post`.
18. **New step, replacing nothing:** construct the drain timer —
```go
timer := qt.NewQTimer2(nil)
timer.SetInterval(30)
timer.OnTimeout(a.dispatch.Drain)
timer.Start(30)
```
19. `execQApplication()` (Task 2) — replaces `a.fyneApp.Run()`.
20. `a.awaitShutdown()` — unchanged.

**Every remaining `fyne.Do`/`fyne.DoAndWait` site** the pre-plan research
enumerated (`checkForUpdate`, `reportCheckResult`, `startUptimeTicker`'s
ticker goroutine) gets the same mechanical replacement:
`fyne.Do(f)` → `a.dispatch.Post(f)`, `fyne.DoAndWait(f)` →
`a.dispatch.PostAndWait(f)`.

**`shutdown(done func())` and `awaitShutdown()`**: preserve every line
verbatim (the concurrent `sync.WaitGroup`-over-both-supervisors logic fixed
earlier this session is framework-agnostic and must not be touched) — the
*only* change anywhere in this function is at its call sites, where the
`done` closure passed in changes from `func(){ a.fyneApp.Quit() }` /
`fyne.Do(a.fyneApp.Quit)` to `func(){ a.dispatch.Post(func(){
qt.QCoreApplication_Quit() }) }`.

**`reportCheckResult`**'s manual-check result window: rebuild its small
content (heading + message) as a plain `QMessageBox` or a small ad-hoc
`QDialog` (whichever the current file's actual complexity warrants — read it
first; the pre-plan research didn't capture its exact widget tree since it
was out of that fork's directive).

- [ ] **Step 1: Read the full current `main.go` in place**, section by
  section, immediately before rewriting each corresponding section — do not
  rely solely on this plan's summary for exact variable names, since the
  research forks intentionally condensed for planning purposes.

- [ ] **Step 2: Write/adapt `main_test.go`**. Read the current file first.
  Keep every test that exercises `shutdown()`/`awaitShutdown()` logic with
  fake supervisors (framework-agnostic, no Fyne types involved) essentially
  unchanged. Delete `TestMainThreadResponsiveTruePath` and any other
  watchdog-specific test (the function under test no longer exists).
  Rewrite `TestOnScreenWakeNeverTouchesTheTunnel` to assert against
  `a.dispatch` having received the expected posted work rather than a Fyne
  test-driver assertion.

- [ ] **Step 3: Run tests to verify the intended-to-fail ones fail**

Run: `CGO_CXXFLAGS=-std=c++17 go test ./cmd/openfortitray/... -v 2>&1 | tail -100`
Expected: multiple failures/build errors reflecting old vs. new API shapes —
this is the expected pre-rewrite state, not a signal to stop.

- [ ] **Step 4: Perform the rewrite** per the bootstrap sequence and
  call-site replacements above.

- [ ] **Step 5: Run the full build and test suite**

Run: `CGO_CXXFLAGS=-std=c++17 go build ./... && CGO_CXXFLAGS=-std=c++17 go test ./... -race`
Expected: **this is the first point in the plan where the full binary must
build successfully.** All packages' tests pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/openfortitray/main.go cmd/openfortitray/main_test.go
git rm cmd/openfortitray/relaunch_unix.go cmd/openfortitray/relaunch_windows.go
git commit -m "feat(main): bootstrap on QApplication, replace fyne.Do with uidispatch, remove freeze watchdog"
```

---

### Task 11: Remove Fyne

**Files:**
- Modify: `go.mod`, `go.sum`
- Delete: any file anywhere in the tree that still imports `fyne.io/...`
  after Tasks 1-10 (expected to be already-zero by this point, but this
  task's job is to *verify* zero, not assume it)

- [ ] **Step 1: Confirm the removal is safe**

Run: `grep -rl "fyne.io" --include="*.go" cmd internal`
Expected: no output. If this prints any file, that file was missed by an
earlier task — fix it here (this is the safety net Global Constraints
promised: "no Fyne import may remain after Task 11", not "before").

- [ ] **Step 2: Remove the dependency**

```bash
CGO_CXXFLAGS=-std=c++17 go mod tidy
```

Verify `go.mod` no longer lists `fyne.io/fyne/v2` or `fyne.io/systray` (or
any `fyne-io/*` indirect dependency) afterward:
`grep -i fyne go.mod` — expect no output.

- [ ] **Step 3: Full verification**

Run: `CGO_CXXFLAGS=-std=c++17 go build ./... && CGO_CXXFLAGS=-std=c++17 go vet ./... && CGO_CXXFLAGS=-std=c++17 go test ./... -race`
Expected: all green.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: remove Fyne dependency, migration complete"
```

---

### Task 12: Manual verification and final review

Not a code-writing task — this is the `run`-skill verification step plus the
SDD final whole-branch review, done by the plan's controller (not dispatched
as a numbered implementer task), per the subagent-driven-development skill's
own process (final review happens once, after all numbered tasks). Listed
here only so the plan's task count is complete and the controller doesn't
treat Task 11 as the finish line.

Checklist for the manual run (macOS, the only platform available
interactively this session):
- Build the real binary, launch it, verify the tray icon appears and is
  clickable.
- Click through Status → Connection → Advanced via the nav rail; verify the
  selected-button highlight follows.
- Trigger at least one validation error (e.g. an invalid gateway) and
  confirm the field highlights and the message appears.
- Exercise Connect/Disconnect against a real or safe test profile if one is
  available; verify the status view's state transitions, uptime timer, and
  activity log all update live.
- Verify native vibrancy is visually present (the window shows blur, not a
  flat color) and the titlebar has no mismatched strip (the exact bug fixed
  earlier this session for the Fyne implementation — confirm it doesn't
  regress).
- Exercise the tray menu: Open, Settings, Auto-connect toggle, View logs,
  Quit (confirm Quit runs `shutdown()` and actually exits, not just hides).
- Screenshot the result for the user's approval, matching this session's own
  established practice of showing real running UI rather than describing it.

Windows/Linux: verified via CI (Task 2's setup) building successfully; full
interactive verification on those platforms is out of session scope and
should be flagged to the user as remaining before those platforms' users get
this release.
