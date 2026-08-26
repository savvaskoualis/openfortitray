# Glass UI — design

**Goal:** every window this app shows (the main shell — Status/Connection/
Advanced — and the update-flow window) renders with real translucent
material behind its content: native vibrancy on macOS, native Mica/Acrylic
backdrop on Windows, native compositor blur where available on Linux (KDE),
and a simulated translucent look everywhere else. Buttons, form fields, and
data rows stay on solid/near-solid backing so nothing loses legibility.

**Status:** design, not implemented. Feasibility for the macOS path is
proven (see "What was spiked" below) — the rest is design, not yet built.

## What was spiked

A throwaway prototype (built and run on this machine, then deleted) proved
the mechanism end to end on macOS:

1. `fyne.Window` can be type-asserted to `driver.NativeWindow` (public Fyne
   API since 2.5) and `.RunNative(func(ctx any))` hands back
   `driver.MacWindowContext{NSWindow uintptr}` — a real `NSWindow*`.
2. A small cgo bridge (same technique this app already uses in
   `dock_darwin.m`) casts that handle and inserts a real
   `NSVisualEffectView` behind the window's content view. This alone was
   **not** sufficient — the window gained rounded corners and a
   transparent titlebar, but the content area stayed a flat opaque panel.
3. The missing piece: Fyne's GL painter clears the framebuffer using
   `theme.Color(theme.ColorNameBackground)`'s alpha channel
   (`internal/painter/gl/painter.go`, `Clear()`). A normal theme's
   background is fully opaque, so Fyne repaints over the vibrancy view
   every frame regardless of the window-level transparency hint
   (`glfw.TransparentFramebuffer`, also required and also not something
   Fyne sets itself). Making `ColorNameBackground` return an alpha-zero
   color let the blur through — confirmed visually: text from a window
   behind the prototype was legibly blurred through the glass content
   area, not just the native chrome.

This means the effect is achievable entirely through public Fyne APIs and
this app's own theme — no Fyne fork, no vendoring a patched dependency.

## Visual direction (validated with mockups)

- **Material:** the "Menu" family — medium tint, high saturation-boosted
  blur. Closest of the options compared to real macOS Liquid Glass and to
  macOS's own menu-bar dropdowns.
- **Layout: glass shell, solid controls.** The window background (nav
  rail + content pane) is glass. Buttons (Connect/Disconnect) stay solid,
  matching today's look. Detail rows (Assigned IP, Protocol, Connected
  since) sit on a dim, near-solid card — not floating directly on the
  blur — so legibility never depends on whatever happens to be behind the
  window at the moment.
- **Light/dark:** both, following system appearance — same as this app
  already does today via `internal/uitheme`.

## Components

| Component | Platform | Responsibility |
|---|---|---|
| `cmd/openfortitray/glass_darwin.{go,h,m}` (new) | macOS | Attach `NSVisualEffectView` (material `.menu`) behind a window's content, given its `NSWindow*` |
| `cmd/openfortitray/glass_windows.go` (new) | Windows | `DwmSetWindowAttribute(hwnd, DWMWA_SYSTEMBACKDROP_TYPE, DWMSBT_TRANSIENTWINDOW)` (Acrylic-family — closer to the Menu material's translucency than Mica) |
| `cmd/openfortitray/glass_linux.go` (new) | Linux | Set `_KDE_NET_WM_BLUR_BEHIND_REGION` on the X11 window (real blur under KDE/Plasma and any compositor honoring the same hint); no-op elsewhere — theme-level translucency is the fallback, not a second blur mechanism |
| `cmd/openfortitray/glass_other.go` (new) | anything else | No-op — window stays fully opaque, theme still applies its (now-translucent) colors, which just look like flat semi-transparent panels with no live blur |
| `internal/uitheme` (modified) | all | `ColorNameBackground` becomes translucent (alpha < 255) per variant; existing `ColorNameHeaderBackground`/`ColorNameInputBackground`/`ColorNameButton` tokens — already near-solid, already used for the update-prompt's version card — become the "glass shell, solid controls" backing without needing a brand-new token |
| `cmd/openfortitray/main.go` (modified) | all | After each window is shown, attempt the platform attach via `driver.NativeWindow`; on any failure (assertion fails, native call errors), log and continue — the app must never fail to show a window over this |

## Mechanism (macOS, proven; other platforms follow the same shape)

1. `glfw.WindowHint(glfw.TransparentFramebuffer, glfw.True)` before the
   window is created. Fyne's own `window.create()` never sets or resets
   this hint, so a value set before Fyne's `glfw.CreateWindow()` call
   persists onto it — confirmed in the spike.
2. `w.Show()`.
3. Type-assert `w.(driver.NativeWindow)`; `RunNative` to get the native
   handle; call into the per-OS glass package to attach the backdrop.
4. Theme's `ColorNameBackground` is transparent, so Fyne's per-frame clear
   no longer paints over the native backdrop.

Windows/Linux mechanically differ (no OpenGL-transparency subtlety — DWM
and the X11 compositor own the backdrop compositing themselves once the
attribute/property is set), but steps 2–4 (show, `RunNative`, transparent
theme) are the same code path across all three; only the platform-specific
attach call differs.

## Fallback behavior

- `driver.NativeWindow` type assertion fails (a driver that doesn't
  implement it, a headless/test environment): skip the native attach
  entirely. The window still renders with the app's (now-translucent)
  theme colors — a flat, semi-transparent look with no live blur, not a
  crash or a broken/invisible window.
- A platform call itself fails (e.g. `DwmSetWindowAttribute` on a pre-Mica
  Windows version, the KDE property has no effect under a non-KDE
  compositor): best-effort, logged, never fatal — same principle this
  app already applies to `helper: not ready` bootstrap failures and DNS
  resolver installation.
- Every attach call runs off the critical path of actually showing the
  window (`Show()` happens first) — a slow or hanging native call must
  never delay the app becoming visible or usable.

## Out of scope

- The macOS tray icon / menu-bar dropdown itself is OS-owned chrome
  (`fyne.io/systray`) — not something this app's own windows can
  glass-style, and not attempted here.
- Windows 10 (pre-Mica) and non-KDE Linux desktops get the simulated
  (theme-only, no live blur) look — this is the intentional fallback, not
  a gap to close later.
- No change to the app's actual behavior/logic — this is a rendering-only
  change to `internal/uitheme` and new small platform-attach files.
