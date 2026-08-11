# Postern — Cross-Platform FortiGate Tray App

**Date:** 2026-08-10
**Status:** Approved (v2 — supersedes terminal-only v1 design)
**Scope:** macOS, Linux, Windows. Native tray app, single Go binary.

## Problem

Team (<15 people) uses the free FortiClient to connect to the company FortiGate
SSL-VPN. The free tier has no auto-connect and no scripting hooks. We need a client
that:

- Shows a tray icon with status and connect/disconnect on macOS, Windows, and Linux
- Auto-starts at login and keeps the tunnel up (reconnect on drop)
- Handles the FortiGate's SAML/SSO authentication
- Is light and dead simple to maintain

## VPN Facts (extracted from FortiClient config on a team MacBook)

| Setting | Value |
|---|---|
| Gateway | `vpn.example.com` |
| Port | `10443` |
| VPN type | SSL-VPN |
| Auth | SAML/SSO, external browser flow (`UseExternalBrowser: 1`) |
| Client tunnel subnet | `10.0.0.0/24` (observed assigned IP `10.0.0.1`) |

Config source on macOS: `/Library/Application Support/Fortinet/FortiClient/conf/vpn.plist`.

The gateway and addresses above are placeholders. Postern ships with **no default
gateway** — it is deployment-specific, so `scripts/install.sh` takes it from
`POSTERN_GATEWAY=host:port` and writes it to the user's `config.json`, and the tray
reports "gateway not set" rather than dialling anything until it is present.

## Tech Choice

**Go + `fyne.io/systray`.** Single static binary (~8–12 MB) per OS, no runtime
dependencies, cross-compiles from macOS, mature tray support on all three platforms.

Rejected: Electron/Tauri (heavy; embedded webview is useless because SAML runs in the
system browser), Python (runtime + packaging pain), Rust (steeper maintenance for the
team).

**Tunnel backend: OpenConnect** (`openconnect --protocol=fortinet`) on all three
platforms — one backend, one flag set. Installed by per-OS install scripts (brew /
apt / dnf; bundled build + wintun on Windows). The Go app never implements PPP/TLS
tunneling itself; it supervises openconnect.

## Architecture

One Go binary `postern`, four packages:

```
postern/
├── cmd/postern/main.go        wiring: config → tray → supervisor
├── internal/auth/             SAML external-browser flow → SVPNCOOKIE
├── internal/tunnel/           openconnect supervisor (spawn, health, backoff restart)
├── internal/autostart/        login-item install/remove per OS
├── internal/tray/             systray menu + status icon
└── internal/config/           static config (gateway, port) + user prefs file
```

### `internal/auth` — SAML flow

1. Start HTTP listener on `127.0.0.1:8020` (FortiClient's conventional redirect port).
2. Open system browser at
   `https://vpn.example.com:10443/remote/saml/start?redirect=1`.
3. User authenticates at IdP (silent if session alive); FortiGate redirects browser to
   `http://127.0.0.1:8020/?id=<auth-id>`.
4. App exchanges the id at `https://<gateway>/remote/saml/auth_id?id=<auth-id>` and
   reads `SVPNCOOKIE` from the response.
5. Returns cookie to caller; listener shuts down. Timeout 5 min → error state in tray.

Because the login happens in the user's real browser, the IdP session persists there;
reconnects are silent until the IdP session expires.

### `internal/tunnel` — supervisor

- Spawns `openconnect --protocol=fortinet <gateway>:<port> --cookie-on-stdin`,
  writes the cookie to stdin.
- Privilege: on macOS/Linux runs `sudo -n openconnect …` (installer added a NOPASSWD
  sudoers rule scoped to the openconnect binary). On Windows the app itself runs
  elevated (see autostart), so openconnect inherits.
- Health: process exit = tunnel down. Auto-reconnect with exponential backoff
  (15 s → 2 min cap). Cookie rejected (openconnect auth failure exit) → re-run SAML
  flow; if that needs interaction the browser window appears.
- States: `Disconnected → Authenticating → Connecting → Connected → Reconnecting`.
  Exposed as a channel the tray consumes.
- "Disconnect" from tray = SIGTERM (Windows: process kill) + supervisor stop; no
  auto-reconnect until user reconnects or next login.

### `internal/tray`

Menu: status line (`Connected — 10.0.0.x` / `Disconnected` / `Connecting…`),
Connect, Disconnect, ✓ Auto-connect at login, View logs (opens log file), Quit.
Icon variants: gray (down), animated/half (connecting), green dot (up), red (error).

### `internal/autostart`

Toggle from tray, default ON at install:
- macOS: `~/Library/LaunchAgents/io.github.savvaskoualis.postern.plist` (`RunAtLoad`, no KeepAlive —
  the app supervises itself)
- Linux: `~/.config/autostart/postern.desktop` (XDG autostart)
- Windows: Scheduled Task at logon, "Run with highest privileges" (this is also the
  elevation mechanism)

### `internal/config`

Gateway/port compiled in as defaults, overridable by
`~/.config/postern/config.json` (`gateway`, `port`, `saml_port`, `openconnect_path`).
User prefs (autostart on/off) stored in the same file. Logs to
`~/.config/postern/postern.log` (platform-appropriate dir via `os.UserConfigDir`).

## Install & Packaging

- Repo ships `install.sh` (macOS/Linux) and `install.ps1` (Windows):
  1. Install openconnect (brew / apt / dnf; Windows: download bundled openconnect
     release + wintun into `%ProgramFiles%\postern`).
  2. Copy the `postern` release binary into place.
  3. macOS/Linux: write `/etc/sudoers.d/postern` (NOPASSWD, exact openconnect path,
     validated with `visudo -c`).
  4. Register autostart; launch the app.
- GitHub-style release artifacts built by `make release`: darwin-arm64, darwin-amd64,
  linux-amd64, windows-amd64.
- Binaries unsigned for now; README documents macOS Gatekeeper right-click → Open.

## Error Handling

- SAML timeout/failure: tray icon red, status shows last error, Connect retries.
- Gateway unreachable: supervisor retries with backoff; tray shows `Reconnecting…`.
- openconnect binary missing: tray error state with "openconnect not found — re-run
  installer" message.
- Cert validation: openconnect fails closed on untrusted cert; error surfaces in tray
  status and log.

## Constraints & Caveats

- **FortiClient conflict:** must not hold a connection simultaneously. README: quit
  FortiClient, disable its login item.
- **SAML:** first-ever connect and IdP-session-expiry connects need one interactive
  browser login. By design; no workaround.
- **Windows elevation:** app runs elevated via scheduled task; acceptable for a small
  trusted team, revisit (proper service split) if org-wide rollout happens.

## Testing

Automated (Go unit tests, `go test ./...`):
- `auth`: SAML flow against a stub HTTP server (redirect + cookie exchange, timeout).
- `tunnel`: supervisor state transitions with a fake backend process (exit → backoff
  restart; auth-failure exit → re-auth; disconnect → no restart).
- `config`: load/save/defaults round-trip.

Manual acceptance per platform:
1. Install script → tray icon appears, first Connect shows browser login, tunnel up,
   internal resource reachable, IP in `10.0.0.0/24`.
2. Toggle Wi-Fi → `Reconnecting…` → `Connected` without user action.
3. Reboot → auto-connects at login (IdP session valid), no interaction.
4. Disconnect from tray → stays down until manual Connect.
5. Expired IdP session → exactly one browser login window, then connected.

## Out of Scope (YAGNI)

- MDM packaging, code signing/notarization
- Split tunneling / route customization (FortiGate pushes routes)
- Multiple VPN profiles
- Storing FortiGate credentials (SAML makes them unnecessary)
