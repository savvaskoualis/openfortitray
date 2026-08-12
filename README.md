# OpenFortiTray

A small cross-platform tray client for FortiGate SSL-VPN gateways that authenticate with
SAML/SSO. It keeps the tunnel up, reconnects on its own, connects at login, and has a
native Settings window — the things the free FortiClient tier does not do.

[![CI](https://github.com/savvaskoualis/openfortitray/actions/workflows/ci.yml/badge.svg)](https://github.com/savvaskoualis/openfortitray/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

OpenFortiTray is the convenience layer around a FortiGate SSL-VPN, not a reimplementation:
`openconnect --protocol=fortinet` moves the packets, your IdP handles credentials, and
OpenFortiTray drives the SAML handshake, supervises the process, and gives it a UI.

- tray icon with tunnel state + Connect / Disconnect
- auto-connect at login and auto-reconnect (backoff 15 s → 2 min)
- SAML login in your system browser — silent while your IdP session is alive
- native **Settings** window: multiple profiles, Basic/Advanced fields, no JSON editing
- one binary per platform; the only runtime dependency is `openconnect` (≥ 8.10, 9.x
  recommended), which every installer pulls in for you

**Authentication:** SAML/SSO via external browser is the only method wired in today, and
the default for new profiles. Username/password and client-cert appear in Settings but
are gated ("not yet supported — use SAML/SSO").

## Install

### macOS — Homebrew (recommended)

```sh
brew install --cask savvaskoualis/tap/openfortitray
```

Then launch **OpenFortiTray**, open **Settings…**, set your gateway `host` and port, and
**Connect**. On the first Connect the app asks for your Mac admin password **once** to
install a small root-owned helper that brings the tunnel up — no separate step. Homebrew
also installs `openconnect` and clears the download quarantine, so there is no Gatekeeper
prompt.

### macOS — `.dmg` (alternative)

Download `OpenFortiTray-<version>.dmg` from [Releases][rel], mount it, drag
`OpenFortiTray.app` to `/Applications`, then clear the quarantine (the app is ad-hoc
signed but not notarized):

```sh
xattr -dr com.apple.quarantine /Applications/OpenFortiTray.app
brew install openconnect   # the .dmg does not bundle it
```

First Connect installs the helper via the same one-time admin prompt.

### Linux

From a checkout of this repo:

```sh
OPENFORTITRAY_GATEWAY=vpn.example.com:10443 bash scripts/install.sh
```

Run as your normal user (it calls `sudo` itself). It installs `openconnect` (apt/dnf/
pacman) if missing, the binary to `/usr/local/bin/openfortitray`, the root-owned helper +
a scoped `sudoers` rule, and a starter `config.json`. Then `openfortitray &`. Needs a
StatusNotifierItem-capable tray and a working OpenGL stack.

### Windows

Download `OpenFortiTray-<version>-Setup.exe` from [Releases][rel] and run it. SmartScreen
(unsigned) → **More info → Run anyway**; approve the UAC prompt. The wizard installs to
`%ProgramFiles%\openfortitray`, adds a Start-menu entry and an elevated logon task, and
bundles `openconnect` (with its DLLs and `wintun.dll`) in `%ProgramFiles%\openfortitray\openconnect`,
so nothing else is needed — Windows has no reliable way to install openconnect otherwise.

Every launch shows a UAC prompt — the app must be elevated to create the wintun adapter.
The login task launches it elevated without a prompt. Open **Settings…** to set your
gateway. The app uses the bundled `openconnect` automatically; to point it at a different
build, set `"openconnect_path"` to its full path in `%APPDATA%\openfortitray\config.json`.

The Windows build bundles a software OpenGL renderer (Mesa's llvmpipe — `opengl32.dll` +
`libgallium_wgl.dll`, installed beside the exe), so the tray works on VMs, RDP sessions,
and other GPU-less Windows where there is no OpenGL driver (otherwise the app dies at
launch with `WGL: driver does not support OpenGL`). Windows loads the app-directory
`opengl32.dll` before the system one, so this affects only OpenFortiTray; the light UI
renders fine in software. The `Setup.exe` installs both DLLs for you — if you download the
bare `openfortitray-windows-amd64.exe` instead, also download `opengl32.dll` and
`libgallium_wgl.dll` from the same release and keep all three in the same folder. Mesa is
MIT-licensed; see [`THIRD_PARTY_LICENSES`](THIRD_PARTY_LICENSES).

> **Before you connect (all platforms):** quit FortiClient — or any other client for this
> gateway — and disable its login item. FortiGate allows only one SSL-VPN session per
> user; two clients fight over the routes and it looks like a flaky network.

[rel]: https://github.com/savvaskoualis/openfortitray/releases/latest

## Usage

The tray menu has the current status, **Connect** / **Disconnect**, **Auto-connect at
login**, **Settings…**, **View logs**, **Check for Updates…**, and **Quit**. Icon colour
tracks state: grey disconnected, yellow connecting, green connected, red error.

The first connect opens your browser for SAML; later connects reuse the IdP session and
are usually silent. If the active profile is not ready to dial (no gateway, bad port, an
unsupported auth method), Connect opens Settings on the offending field instead of
throwing a cryptic error.

**Updates.** OpenFortiTray checks GitHub for newer releases in the background. When one
exists the tray shows **Update to vX.Y.Z & Restart**; clicking it upgrades in place
(`brew upgrade` on macOS, the verified `Setup.exe` on Windows) and relaunches.

## Configuration

The **Settings window is the primary way to configure**; you do not need to edit JSON.
For reference, `config.json` (schema v2, multi-profile) and `openfortitray.log` live in:

| OS | directory |
| --- | --- |
| macOS | `~/Library/Application Support/openfortitray/` |
| Linux | `~/.config/openfortitray/` |
| Windows | `%APPDATA%\openfortitray\` |

Key profile fields: `gateway` (host, no scheme/port — no default), `port` (`10443`),
`saml_port` (`8020`), `auth.method` (`saml`), `realm`, `dtls` (`true`; off →
`--no-dtls`), `dual_stack` (`false`; off → `--disable-ipv6`), `server_cert.mode`
(`warn`/`trust`/`pin`, with a sha256 `pin`), `split_dns`. A minimal config:

```json
{
  "schemaVersion": 2,
  "activeProfile": "Default",
  "profiles": [
    { "name": "Default", "gateway": "vpn.example.com", "port": 10443,
      "auth": { "method": "saml" }, "dtls": true, "server_cert": { "mode": "warn" } }
  ],
  "autostart": true
}
```

Split-DNS domains are captured and validated but not yet installed as a scoped resolver —
running alongside Tailscale/another VPN may need a manual `/etc/resolver` entry until that
automation ships.

## Privilege

`openconnect` must run as root to create the tunnel and edit routes. On macOS/Linux
OpenFortiTray installs a root-owned helper (`/usr/local/libexec/openfortitray-tunnel`)
reached through a `sudoers` rule scoped to that one path. The helper takes only
`start <host:port> [flag…]` and `stop`; the gateway is pattern-validated and extra flags
are checked against an exact allowlist (`--no-dtls`, `--disable-ipv6`,
`--servercert <fingerprint>`), so the passwordless rule cannot be turned into arbitrary
root. Full threat model in the header comments of
[`scripts/openfortitray-tunnel`](scripts/openfortitray-tunnel) and
[`scripts/install.sh`](scripts/install.sh). On Windows there is no helper — the app runs
elevated and calls `openconnect` directly.

## Troubleshooting

Start with **View logs**. Common cases:

- **Connect keeps opening Settings** — the active profile is not ready; the flagged field
  names the fix.
- **Helper not effective (macOS/Linux)** — `sudo -n /usr/local/libexec/openfortitray-tunnel stop`
  must print nothing and exit 0. If it prompts, re-run `scripts/install.sh` as your normal
  user (not under `sudo`).
- **Routing left broken after a disconnect** — `sudo /usr/local/libexec/openfortitray-tunnel stop`
  (idempotent) clears it. On Windows a reconnect/reboot restores it.
- **Port 8020 in use** — free it or change the profile's SAML port (your gateway must
  allow the new redirect port).

## Uninstall

Quit the app first.

**macOS**
```sh
launchctl bootout gui/$(id -u)/io.github.savvaskoualis.openfortitray 2>/dev/null
brew uninstall --cask openfortitray 2>/dev/null || sudo rm -rf /Applications/OpenFortiTray.app
sudo rm -f /usr/local/libexec/openfortitray-tunnel /etc/sudoers.d/openfortitray
rm -rf ~/Library/Application\ Support/openfortitray
```

**Linux**
```sh
sudo rm -f /usr/local/bin/openfortitray /usr/local/libexec/openfortitray-tunnel /etc/sudoers.d/openfortitray
rm -rf ~/.config/openfortitray ~/.config/autostart/openfortitray.desktop
```

**Windows** (elevated PowerShell)
```powershell
schtasks /Delete /TN "OpenFortiTray" /F
Remove-Item -Force "$env:APPDATA\Microsoft\Windows\Start Menu\Programs\OpenFortiTray.lnk"
Remove-Item -Recurse -Force "$env:ProgramFiles\openfortitray","$env:APPDATA\openfortitray"
```

## Building from source

Fyne renders through cgo + OpenGL, so each OS builds on its own native toolchain (macOS:
Xcode CLT; Linux: `gcc` + `libgl1-mesa-dev` + `xorg-dev`; Windows: MinGW `gcc`). CI builds
the full three-OS release matrix; a `v*` tag publishes signed-per-runner `SHA256SUMS`, the
`.dmg`, and the `Setup.exe`.

```sh
make build    # go build -o openfortitray ./cmd/openfortitray
make test     # go vet ./... && go test -race ./...
make app      # macOS: assemble dist/OpenFortiTray.app
make dmg      # macOS: build the drag-to-Applications .dmg
```

## License

MIT. See [LICENSE](LICENSE).
