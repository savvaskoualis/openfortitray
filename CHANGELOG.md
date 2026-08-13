# Changelog

All notable changes to OpenFortiTray. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions are the published `v*`
tags. Dates are the release date.

## [Unreleased]

_Nothing yet._

## [0.1.32] — 2026-08-13

### Fixed
- **The tray menu updates while it is open.** It used to show whatever it showed
  when you opened it — click Connect and the row kept saying "Connecting…" until you
  closed and reopened the menu. The rows are now updated in place rather than the
  whole menu being rebuilt, which is what an already-open menu can pick up.
- **Windows: openconnect's console window no longer appears.** A black
  `openconnect.exe` window sat on top of your work for as long as the VPN was up —
  and closing it, or pressing Ctrl-C in it, killed the tunnel.

## [0.1.31] — 2026-08-13

### Fixed
- **Windows: the same cookie truncation as 0.1.30.** Windows runs openconnect
  directly instead of through the privileged helper, so the 0.1.30 fix did not
  reach it. It now passes the cookie in a private file there too, so long cookies
  survive.
- **The connect status no longer blames your gateway.** It used to say "the VPN
  allows one session per user" while retrying — a guess that later evidence
  contradicted. It now says only what is known: "gateway refused the session —
  retrying".
- **macOS: the helper prompt says "Update" when a helper is already installed**,
  and the log records why a connect is waiting on it, instead of the app appearing
  to sit idle.
- **A stale saved session no longer blocks connecting for ~100 seconds.** 0.1.28
  made the retries re-send the same cookie to stop a browser tab opening per
  attempt; with the truncation above fixed, a refused cookie really is stale and
  only a fresh login recovers, so re-sending it just stalled the connect. A refused
  cookie is now replaced immediately, and a connect that cannot succeed stops after
  a few minutes with "couldn't connect — click Connect to try again".

## [0.1.30] — 2026-08-13

### Fixed
- **"Cookie was rejected by server" — the real cause.** openconnect reads the
  cookie passed on its standard input into a 1024-byte buffer and silently
  truncates anything longer. This gateway's login cookies are routinely longer
  (1288 bytes seen) and vary in length from login to login, so connects failed
  seemingly at random and succeeded whenever a login happened to produce a short
  enough cookie — which is what "it connects after three browser tabs" was. Proven
  side by side: the same cookie was refused when passed on stdin and accepted when
  not. The privileged helper now hands it over in a root-only file (never the
  command line, where it would be visible in `ps` for the life of the tunnel).
  On first connect after updating, macOS/Linux will ask for your administrator
  password once to install the corrected helper.

### Note
- Earlier releases today attributed these failures to the gateway holding a
  previous session, and 0.1.26–0.1.29 were shaped around that. The reconnect delays
  behind that theory were mostly this bug.

## [0.1.29] — 2026-08-13

### Changed
- **A slow connect now says why, and polls instead of backing off.** This gateway
  allows one SSL-VPN session per user and refuses everything until it releases the
  previous one — a median of 25s after a disconnect, longer while another machine
  signed in as the same user still holds it. The app now retries every 5s for the
  first ~100s (an escalating backoff made it miss the moment the session became
  free) and shows "gateway busy — the VPN allows one session per user" rather than
  a bare "Connecting…" that reads as stuck.

### Fixed
- The app no longer logs "session ended on the gateway" when the gateway did not
  accept the logout. It answers an unauthenticated logout with a redirect to the
  identity provider, which was being counted as success.

## [0.1.28] — 2026-08-13

### Fixed
- **One browser tab per connect instead of three.** The connect-time retries each
  re-ran the SAML login — three logins in eleven seconds when the gateway was slow
  to release a previous session. Those extra logins never helped: a gateway that is
  refusing because it still holds a session refuses brand-new cookies just as
  readily. The retries now reuse the cookie silently, and cover a wider window
  (~20s) before anything louder happens.

## [0.1.27] — 2026-08-13

### Fixed
- **Windows: the in-app update never actually ran** — the app closed and nothing
  happened. The updater was started with `DETACHED_PROCESS`, which leaves the
  child with no console, and `powershell.exe` can exit immediately without one
  (the app itself has no console to inherit, being a GUI build). It now gets its
  own hidden console, runs from a script file instead of a fragile one-line
  command, logs every step to `update.log`, and always brings the app back — via
  the scheduled task, or directly if that fails — so a failed update can never
  leave you with no app running.
- **No more endless browser tabs when a connect keeps failing.** A connect that
  never came up retried forever and re-ran the login every round, opening a tab
  each time. It now keeps its cookie between attempts (re-authenticating only
  occasionally, since a gateway that is refusing because it still holds a previous
  session refuses fresh logins too), and after several minutes stops with
  "couldn't connect — click Connect to try again". Reconnection after a session
  that *was* working stays unlimited.

## [0.1.26] — 2026-08-13

### Fixed
- **Reconnecting is no longer blocked for minutes after a disconnect.** openconnect
  has no logout for the Fortinet protocol (it supports Juniper and GlobalProtect
  only), so closing the tunnel left the session established on the FortiGate until
  the gateway timed it out — and because the gateway allows one SSL-VPN session per
  user, every reconnect during that window was refused. Measured: five separate
  fresh logins rejected across 3.5 minutes after a clean disconnect. The app now
  sends the logout the gateway expects when the tunnel goes down, as FortiClient
  does, so the session is released immediately and the next connect works first
  try. It is best-effort and bounded, so an unresponsive gateway cannot delay quit.

## [0.1.25] — 2026-08-13

### Fixed
- **Connect is ~12s faster.** 0.1.21 re-sent a rejected session cookie four times,
  three seconds apart, before falling back to a fresh login. Measuring 43 real
  attempts showed why that was wrong: a reused cookie was refused on all 30
  attempts (a FortiGate session cookie is bound to its session, so one it has
  rejected can never start working), while a fresh login was accepted on the first
  try every single time. The re-sends only delayed the login that actually works.
  A rejected cookie now goes straight to a fresh login, and the dead one is
  deleted so the next connect does not retry it.

### Removed
- **The automatic "skip DTLS" behaviour from 0.1.23 is gone.** The same
  measurements showed `--no-dtls` prevents authentication on this gateway
  (10 fresh cookies refused with it, interleaved with successes without it). The
  ~5s DTLS handshake wait it was meant to avoid is real, but it needs the
  gateway's UDP port opened, not this flag. If you have `dtls` turned off in a
  profile and cannot connect, turn it back on.

## [0.1.24] — 2026-08-13

### Fixed
- **The app no longer leaks its VPN session when it exits on a signal**
  (`launchctl bootout`, logout, restart, `kill -TERM`). fyne installs its own
  SIGINT/SIGTERM handler that ends the run loop immediately, and Go delivers a
  signal to every handler, so the process could exit while the tunnel teardown was
  still running — openconnect never sent its logout, and the gateway kept the
  session and refused every new cookie until it timed out server-side, sometimes
  for many minutes. This is the "we get logged out a lot" and "it just won't
  connect" behaviour. The process now waits for the teardown before leaving.

### Changed
- **Skipping DTLS is no longer automatic.** 0.1.23 started passing `--no-dtls` to
  gateways that had refused a DTLS tunnel, to avoid a measured ~5s handshake wait.
  In testing, a run of connection failures overlapped that change and could not be
  separated from a second cause active at the same time (a leaked session, fixed
  above). Rather than leave a flag that might prevent connecting enabled by
  default, the app now only notes the opportunity in the log; turning DTLS off is
  the explicit `dtls` profile toggle, as before.

## [0.1.23] — 2026-08-13

### Fixed
- **Connect is ~6s faster on networks that block the VPN's UDP port.** Timing the
  handshake showed openconnect had the full tunnel config in 0.32s, then spent
  5.0s waiting for a DTLS (UDP) handshake nothing would answer and re-ran the
  config exchange over HTTPS — 6.7s to a usable tunnel, on every connect. The app
  now notices when a gateway refuses DTLS, remembers it, and stops asking, so
  later connects go straight over HTTPS. The record expires after a week, so a
  network that starts allowing UDP gets DTLS back automatically. The `dtls`
  profile toggle still overrides this.

## [0.1.22] — 2026-08-13

### Changed
- **Connect timing is now visible in the log.** openconnect's progress lines are
  mirrored into `openfortitray.log` with elapsed seconds, from launch until the
  tunnel comes up (and only until then — after that the same stream is ordinary
  traffic chatter). A connect that feels slow can now be attributed to a phase:
  TLS, the gateway's config exchange, tunnel setup, or the routing script.

## [0.1.21] — 2026-08-13

### Fixed
- **The browser no longer opens several times before the VPN connects.** On a
  gateway that permits one SSL-VPN session per user, a connect attempted while
  the previous session is still being reaped is refused — and it refuses a
  freshly minted cookie just as readily as an old one (logs show four fresh
  cookies rejected in a row, then an identically produced fifth accepted). The
  app used to treat every refusal as a dead cookie and run SAML again, costing a
  browser tab per attempt. It now re-sends the same cookie a few seconds apart
  while the gateway finishes reaping, and only re-authenticates if that genuinely
  fails. A still-valid stored session is no longer discarded either.
- **"Update available" now means installable.** On macOS the update check reads
  GitHub releases while the install runs `brew upgrade --cask`, so a release
  published before the Homebrew tap caught up produced an offer that brew
  declined with "the latest version is already installed" — nothing happened.
  The tap now bumps itself automatically on every release, and the app withholds
  the offer until the cask is actually ready (failing open if the tap cannot be
  read, so a network problem never hides a real update).

## [0.1.20] — 2026-08-13

Stability and noise release: fewer logins, calmer status, notifications when it
matters.

### Added
- **Session reuse — no browser on every connect.** The SVPNCOOKIE is stored in
  platform-native, user-scoped secret storage (macOS login keychain, Windows
  DPAPI-encrypted file, `0600` file on Linux) and reused on reconnect and across
  restarts; SAML runs only when the gateway rejects the stored cookie. Toggle
  under Settings → Advanced ("Reuse session to avoid re-login", on by default);
  turning it off, or changing a profile's gateway or auth method, deletes the
  stored cookie. The value is never written to `config.json` or the log.
- **Desktop notifications** on the transitions worth interrupting for: connected
  (with the assigned IP), an established tunnel dropping, and a terminal
  failure. Fires on state changes only, so a reconnect does not toast once per
  retry, and a user-requested disconnect stays silent.

### Changed
- **Status text is human again.** The tray and settings strip no longer show
  openconnect's multi-line stderr, its route-teardown output, or the helper's
  "not owned by root" warning — those stay in the log. You get
  "Reconnecting…", "connection lost — reconnecting", or a one-line install hint.
- **No more scary red flashes during a normal connect.** The first-cookie
  rejection dance this gateway does stays yellow (Connecting/Reconnecting); red
  is reserved for a genuinely terminal failure.
- **Stops instead of spinning when the session was taken.** On gateways that
  allow one SSL-VPN session per user, another device logging in kills this
  session. After a run of post-connect cookie rejections the app now stops with
  "VPN session ended — click Connect to sign in" rather than re-running SAML
  unattended (which, overnight, just times out on an untouched browser tab).
- A failed sign-in reports "sign-in didn't complete — click Connect" instead of
  the raw SAML error.

### Fixed
- The Linux/other session store now re-asserts `0600` on every write:
  `os.WriteFile` applies its mode only when creating a file, so a store file
  that already existed with wider permissions kept them.

## [0.1.19] — 2026-08-12

### Added
- **Windows bundles openconnect 9.12 + wintun + vpnc-script** inside `Setup.exe`.
  openconnect is built from source once (`build-openconnect` workflow) into a
  pinned, sha256-verified zip that the release just downloads — MSYS2 has no
  openconnect package and winget is unavailable on locked-down Cloud PCs. The
  tunnel backend now needs no separate install. See `THIRD_PARTY_LICENSES`.

## [0.1.18] — 2026-08-12

### Added
- **Windows now bundles `openconnect` + `wintun`** inside `Setup.exe` (collected
  from MSYS2 with its full DLL closure, CI-verified via `openconnect --version`),
  so the tunnel backend needs no separate install — winget was unavailable on
  locked-down Cloud PCs. The app resolves the bundled binary automatically. See
  `THIRD_PARTY_LICENSES` (openconnect LGPL-2.1 + deps + wintun).

## [0.1.17] — 2026-08-12

### Fixed
- **Windows: opening any window (Settings, the update prompt) crashed the app on
  GPU-less hosts (Cloud PC / RDP).** Mesa's default llvmpipe software driver
  JIT-crashes on the first GL draw there; force the pure-C `softpipe` driver
  (`GALLIUM_DRIVER=softpipe`) at startup on Windows. The tray already worked; now
  windows render too.

## [0.1.16] — 2026-08-12

### Changed
- The update prompt is now its own dedicated window instead of appearing on top
  of (and surfacing) the Settings window.

## [0.1.15] — 2026-08-12

### Changed
- More Windows startup diagnostics (fyne `OnStopped` hook, settings-window-show
  and tray-reassert breadcrumbs) to pinpoint a silent post-startup exit on
  GPU-less/Cloud-PC Windows.

## [0.1.14] — 2026-08-12

### Fixed
- **Windows single-instance is now a named mutex, not a pidfile.** The pidfile's
  liveness check (`os.FindProcess`) succeeds for a dead-or-reused pid on Windows,
  so a crash left a stale lock that made every later launch exit with "another
  instance is already running" — the app could never start again. A session-local
  named mutex is released automatically by the kernel on process death.
- **Windows tray icon now appears.** The icon was set in `Setup` before fyne's
  run loop, when the Windows systray isn't ready yet ("tray not ready yet"), so no
  icon showed and the app looked like it never launched. It is now re-asserted
  from the `OnStarted` lifecycle hook, once the native tray is live.

## [0.1.13] — 2026-08-12

### Added
- Startup and crash diagnostics: the app logs a build/platform stamp on launch, a
  "run loop returned" line on clean exit, and captures a main-goroutine panic (with
  stack) to the log. On Windows the `-H=windowsgui` build has no console, so the
  process stderr is redirected to the log file — a Go runtime panic or a cgo/OpenGL
  crash now lands in `openfortitray.log` instead of vanishing. Helps diagnose
  silent GUI exits (e.g. on GPU-less VMs).

## [0.1.12] — 2026-08-12

### Added
- **Windows software OpenGL:** the Windows build bundles Mesa's llvmpipe renderer
  (`opengl32.dll` + its `libgallium_wgl.dll` backend, pinned + sha256-verified in
  CI) beside the exe, so the app runs on VMs, RDP sessions, and GPU-less Windows
  where there is no OpenGL driver (previously died at launch with `WGL: driver
  does not support OpenGL`). Installed by `Setup.exe` and `install.ps1`.
- **Update UX:** the tray icon shows a red badge when an update is available, and
  a popup dialog ("Update & Restart" / "Later") appears once per new version — on
  top of the existing "Update to … & Restart" menu item.
- **App icons:** a real `.icns` on the macOS bundle and an embedded `.ico` on the
  Windows exe (previously the generic file icon).

### Fixed
- The in-app updater now runs `brew update` before `brew upgrade` on macOS, so it
  actually sees a new release in the custom tap instead of no-op'ing with "latest
  already installed".

## [0.1.11] — 2026-08-12

### Fixed
- **Windows:** the installer's post-install "Launch OpenFortiTray" step failed
  with `code 740, requires elevation`. The exe ships a `requireAdministrator`
  manifest, and Inno launched it via `CreateProcess` as the non-elevated user,
  which cannot start an elevation-required exe. Now launched with `shellexec`
  (`ShellExecuteEx`), which honours the manifest and raises the UAC prompt — the
  same path the Start-menu shortcut already used.

## [0.1.10] — 2026-08-12

### Fixed
- Windows CI test portability (no functional change): an OS-aware install-hint
  assertion and skipping the POSIX `0600` file-mode check on Windows (Go reports
  `0666` there). Green CI on macOS, Linux, and Windows.

## [0.1.9] — 2026-08-12

### Fixed
- **Windows:** the permanent-failure hint was too long for the tray's 60-rune
  first-line clip and would be truncated. Shortened to `reinstall as
  Administrator`; hint tests are now OS-aware.

## [0.1.8] — 2026-08-12

### Added
- The build version is shown in the tray menu, under the title row.
- **One-click background auto-update.** The app checks GitHub for newer releases
  (~30 s after launch, then every 6 h). When one exists the tray offers
  "Update to vX.Y.Z & Restart"; applying it upgrades in place — `brew upgrade`
  on macOS, the SHA256-verified `Setup.exe` on Windows — and relaunches. Manual
  and Linux installs open the releases page instead. `dev`/untagged builds never
  self-update.

## [0.1.7] — 2026-08-12

### Fixed
- **Connect on the first try.** On a startup cookie rejection the supervisor
  re-minted a fresh SAML cookie instead of re-sending the rejected one, removing
  the "connects on the 2nd attempt after ~24 s" stall.

## [0.1.6] — 2026-08-11

### Added
- **macOS first-run helper bootstrap:** the app installs its privileged helper on
  the first Connect via a single admin-password prompt — no separate
  `install.sh`/`install-helper.sh` step needed.
- **Windows always runs elevated** via an embedded `requireAdministrator`
  manifest, so `openconnect` can create the wintun adapter from any launch path
  (previously only the login scheduled task was elevated, so a Start-menu launch
  could not bring the tunnel up).

### Security
- Hardened the root-owned helper installer per an adversarial review (verified
  bytes are the installed bytes; symlink-resolved ancestry checks).

## [0.1.5] — 2026-08-10

### Fixed
- Reliable startup connect: single-instance lock, clean logout of a stale
  session, and a quiet early retry, fixing the recurring "cookie rejected" on
  launch.

## [0.1.1] – [0.1.4] — 2026-08-10

- Homebrew cask distribution (with quarantine stripped in `postflight`), ad-hoc
  code signing so the unsigned `.app` launches on Apple Silicon, the Windows Inno
  Setup installer, the GitHub Actions release matrix, and installer fixes.

## [0.1.0] — 2026-08-10

- Initial release: cross-platform FortiGate SSL-VPN menu-bar client. SAML/SSO via
  external browser, `openconnect --protocol=fortinet` tunnel with an
  auto-reconnecting supervisor, auto-connect at login, a native Fyne Settings
  window with multiple profiles, and per-OS installers.
