# Changelog

All notable changes to OpenFortiTray. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions are the published `v*`
tags. Dates are the release date.

## [Unreleased]

_Nothing yet._

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
