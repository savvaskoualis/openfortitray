# UI revamp — design

**Goal:** the app should look like a product rather than a toolkit demo. Two parts:
a designed theme applied everywhere, and a real status window — the thing every
comparable client (Tailscale, FortiClient, Cloudflare WARP) has and we do not.

**Decisions taken:** add a status window (not a restyle alone); the theme follows
the OS light/dark setting with one restrained accent.

## Why a status window, beyond looks

It fixes a defect we cannot otherwise fix. The tray menu is a *native* OS menu, and
fyne refreshes it by tearing the whole menu down and rebuilding it
(`SetSystemTrayMenu` → `systray.ResetMenu()`), which the OS ignores while the menu
is open. That is the "popover doesn't update, must be closed first" bug, and
`internal/tray/tray.go` carries a KNOWN LIMITATION comment saying it cannot be
fixed at that layer — an attempt to fix it in 0.1.32 froze the menu on both macOS
and Windows and was removed.

A fyne window has no such constraint: it redraws on every state change while
visible. So the status window is where live state actually works, and the tray menu
can go back to being what native menus are good at — a short list of actions.

## Constraints this design has to live inside

These are properties of fyne 2.8 and of how we ship, not preferences:

- **No font weight axis.** fyne's `Font(TextStyle)` offers regular, bold, italic,
  monospace — nothing between. Hierarchy comes from size, weight, and color only.
  fyne 2.8 already bundles Inter for the sans face, so no font is embedded and the
  binary does not grow.
- **The tray menu stays unstyled.** It is an OS menu. Nothing below applies to it.
- **Software rendering is a real target.** Windows VMs and RDP sessions run on
  bundled Mesa llvmpipe. Avoid continuous animation and per-frame relayout; a
  1 Hz uptime tick is the fastest thing on the screen.
- **No CSS.** Every color and size comes from the theme tokens, so a widget that
  needs a color we did not tokenise is a widget we do not use.

## The theme

One `fyne.Theme` implementation in a new `internal/uitheme`, wrapping
`theme.DefaultTheme()` and overriding tokens. `Color` takes the variant fyne
resolves from the OS, so light and dark are two token sets, not two code paths.

Neutrals are biased cool by a few points rather than being pure grey — a pure
mid-grey is the thing that reads as unconsidered. The accent is used only for
primary actions, focus, and selection. **State color is separate from the accent**
and only ever appears in the status dot and its label.

| Token | Light | Dark |
|---|---|---|
| `Background` | `#F6F7F9` | `#16181C` |
| `HeaderBackground` (cards) | `#FFFFFF` | `#1E2128` |
| `MenuBackground`, `OverlayBackground` | `#FFFFFF` | `#1E2128` |
| `Foreground` | `#171A1F` | `#EDEFF2` |
| `PlaceHolder`, `Disabled` (muted text) | `#5C6470` | `#9AA2AE` |
| `Separator`, `InputBorder` | `#E2E5EA` | `#2C313A` |
| `Primary` (accent) | `#2F6FEB` | `#5B93F5` |
| `ForegroundOnPrimary` | `#FFFFFF` | `#0E1013` |
| `InputBackground` | `#FFFFFF` | `#22262E` |
| `Hover` | `#00000010` | `#FFFFFF12` |
| `Success` | `#2E9E5B` | `#41BE77` |
| `Warning` | `#B87514` | `#E0A140` |
| `Error` | `#C4362F` | `#E86A62` |

Sizes: `Text` 13, `CaptionText` 11, `SubHeadingText` 15, `HeadingText` 20,
`Padding` 5, `InnerPadding` 10, `CardRadius` 8, `ButtonRadius` 6, `InputRadius` 6,
`SeparatorThickness` 1.

Uppercase section labels (`CONNECTION`, `STARTUP`) use `CaptionText` with muted
foreground and letter-spacing faked by the only means fyne offers — a space between
characters is *not* acceptable (it breaks screen readers and text selection), so
these are plain uppercase captions with no tracking. Noted because it is a real
limitation, not an oversight.

## The status window

680 × 520, resizable, minimum 520 × 440. Opened from the tray ("Status…"), from a
double-click on the tray icon where the OS supports it, and shown on first run
after the helper is installed. **Closing it hides it; it never quits the app** —
same contract the tray already implies.

```
┌─ OpenFortiTray ───────────────────── ─ □ ✕ ┐
│                                            │
│   ●  Connected                             │
│      vpn.example.com · 00:14:32            │
│                                            │
│   ┌──────────────────────────────────────┐ │
│   │ Assigned IP      10.0.0.88           │ │
│   │ Gateway          vpn.example.com:10443│ │
│   │ Protocol         Fortinet · DTLS off │ │
│   │ Connected since  14:22                │ │
│   └──────────────────────────────────────┘ │
│                                            │
│   [  Disconnect  ]        [ Settings ]     │
│                                            │
│   ▾ Activity                               │
│     14:22:04  Connected                    │
│     14:22:01  Authenticated                │
│     14:21:58  Signing in…                  │
└────────────────────────────────────────────┘
```

**Header.** A `canvas.Circle` in `Success` / `Warning` / `Error`, the state in
`SubHeadingText`, and a muted sub-line: gateway host plus uptime when connected,
the `friendlyDetail` text otherwise. This is the one place state color appears.

**Detail card.** A two-column grid on `HeaderBackground` with `CardRadius`. Values
are right-aligned and use `tabular-nums`' equivalent — fyne has no numeric variant
selector, so the IP and clock use the bundled monospace face to stop digits from
jittering as the uptime ticks.

**Buttons.** Primary action is state-dependent (Connect / Disconnect / Cancel) and
is the only `HighImportance` button on screen. Settings is `MediumImportance`.

**Activity.** A collapsible list of the last 50 state transitions, timestamped,
newest first — the tunnel event stream, not the raw log. "Open log file…" sits at
its foot for the real thing.

### What the window can honestly show — and one row that has to go

Everything above comes from data the app already has: `tunnel.Event` carries the
state and, when `Connected`, the assigned IP in `Detail`; the gateway, port, and
DTLS setting come from config; uptime comes from the first `Connected` event.

**The approved mockup had a "Sent / Received" row. It is not in this design.** We
have no byte counters: openconnect does not print them on the progress stream we
read, so the numbers would have to come from per-OS interface statistics —
`/proc/net/dev` on Linux, a `sysctl` route dump on macOS, `GetIfEntry2` on Windows —
plus reliable identification of the tunnel device, which we do not currently track.
That is three platform files and a new data path. Showing a permanently-zero
counter instead would be worse than showing nothing, so the row is replaced by
"Connected since" and traffic counters become a separate, later piece of work if
you want them.

## Shared view model

The tray and the status window must never disagree about state. Today
`internal/tray` derives its labels from `tunnel.Event` internally, so a second
consumer would duplicate that logic.

Extract the derivation into `internal/uistate`: `View{State, Title, Detail,
GatewayHost, AssignedIP, ConnectedAt, CanConnect, CanDisconnect}` built from an
`Event` plus config. `internal/tray` renders it to menu items; the status window
renders it to widgets. One place decides what "Reconnecting" says.

The activity ring lives here too, so both surfaces see the same history.

## Settings and update windows

Same theme, and the structural fixes that make the theme visible:

- Settings keeps its Basic/Advanced tabs (the sidebar option was not chosen) but
  gains uppercase section captions, grouped fields, consistent `InnerPadding`, and
  a right-aligned `[Cancel] [Save]` footer with Save as the only high-importance
  button.
- The update window (440 × 170 today) gets the same treatment and its version
  numbers in the monospace face.
- First-run helper dialogs inherit the theme with no structural change.

## Testing

fyne's `test` package drives widgets without a display, which is what CI has. Test
the logic, not the pixels:

- `internal/uistate`: table tests over every `State` → expected `View`. This is
  where the behaviour lives, and it needs no toolkit at all.
- `internal/uitheme`: every token defined in the table above returns a non-nil,
  non-default value for **both** variants — the guard against a color that only
  exists in one theme, which is the classic way half a UI turns unreadable.
- The status window: build it with `test.NewApp()`, feed it events, assert on
  widget text and on which button is enabled. No golden images — they break on
  every font metric change and teach nothing.

## Out of scope

Traffic counters (above). Any change to the tray menu's structure. Localisation.
A macOS dock presence. Icon redesign — the existing tray glyphs stay.
