# FortiGate VPN Client — Cross-Platform Terminal Solution

**Date:** 2026-08-10
**Status:** Approved
**Scope:** macOS + Linux. Windows explicitly out of scope (no Windows machines in team).

## Problem

Team (<15 people, all currently on MacBooks, Linux expected) uses the free FortiClient
to connect to the company FortiGate SSL-VPN. The free tier has no auto-connect and no
scripting hooks. We need a client that:

- Auto-starts at login and keeps the tunnel up (reconnect on drop)
- Works on macOS and Linux
- Handles the FortiGate's SAML/SSO authentication
- Terminal-only is acceptable (tray icon deferred to a possible phase 2)

## VPN Facts (extracted from FortiClient config on a team MacBook)

| Setting | Value |
|---|---|
| Gateway | `securityhub.hyperio.cloud` |
| Port | `10443` |
| VPN type | SSL-VPN |
| Auth | SAML/SSO, external browser flow (`UseExternalBrowser: 1`) |
| Client tunnel subnet | `10.212.134.0/24` (observed assigned IP `10.212.134.1`) |

Config source on macOS: `/Library/Application Support/Fortinet/FortiClient/conf/vpn.plist`.

## Approach

Glue two maintained open-source tools together; no custom daemon code.

- **openfortivpn** — Fortinet SSL-VPN client (PPP-based). Installed via Homebrew on
  macOS, distro package on Linux. Requires root to create the tunnel.
- **openfortivpn-webview** — opens a browser window for the SAML login, captures
  `SVPNCOOKIE`, prints it to stdout. Its browser profile persists IdP session cookies,
  so after the first interactive login, subsequent connects complete silently until the
  IdP session expires.

Connect pipeline:

```
openfortivpn-webview securityhub.hyperio.cloud:10443 \
  | sudo openfortivpn -c <config> --cookie-on-stdin
```

Unattended auto-connect is only impossible when the IdP session has expired — then a
login window pops up once. Accepted limitation of SAML.

## Repository Layout

```
hyp-vpn/
  install.sh                      # one-shot installer (macOS + Linux)
  bin/vpn                         # user command: up | down | status | logs
  config/openfortivpn.conf        # host, port, trusted-cert (template)
  macos/com.hyperio.vpn.plist     # launchd LaunchAgent template
  linux/hyp-vpn.service           # systemd user unit template
  docs/superpowers/specs/         # this spec
  README.md                       # install + usage + FortiClient caveat
```

## Components

### `install.sh`

1. Detect OS (Darwin / Linux + package manager: apt, dnf, pacman).
2. Install `openfortivpn` (brew / distro package) and `openfortivpn-webview`
   (download release binary; brew cask not available for it).
3. Write `/etc/openfortivpn/hyp-vpn.conf` from template (host, port, username).
4. Probe the gateway TLS certificate; if not publicly trusted, pin its SHA-256 digest
   into the config as `trusted-cert`.
5. Add a sudoers drop-in (`/etc/sudoers.d/hyp-vpn`, validated with `visudo -c`):
   current user, NOPASSWD, exact path of the `openfortivpn` binary only.
6. Install and load the autostart unit:
   - macOS: `~/Library/LaunchAgents/com.hyperio.vpn.plist`, `launchctl bootstrap`
   - Linux: `~/.config/systemd/user/hyp-vpn.service`, `systemctl --user enable --now`
7. Symlink `bin/vpn` into `/usr/local/bin/vpn` (or `~/.local/bin` on Linux).

Idempotent: safe to re-run; re-running updates config and units in place.

### `bin/vpn`

Single POSIX-ish shell script (bash), subcommands:

- `vpn up` — runs the webview → openfortivpn pipeline in the foreground of the
  supervising unit; detects already-connected state and exits 0.
- `vpn down` — stops the unit (`launchctl bootout` / `systemctl --user stop`) which
  terminates openfortivpn cleanly (SIGTERM → PPP teardown).
- `vpn status` — reports: unit running?, tunnel interface up (utun*/ppp0 with
  10.212.134.x address)?, gateway reachable?
- `vpn logs` — tails `~/Library/Logs/hyp-vpn.log` (macOS) / `journalctl --user -u
  hyp-vpn` (Linux).

### Autostart units

- **macOS LaunchAgent:** `RunAtLoad=true`, `KeepAlive.SuccessfulExit=false` with
  `ThrottleInterval=15` — relaunches the pipeline when openfortivpn exits (network drop,
  cookie expiry). Runs in the user GUI session so the webview can display.
- **Linux systemd user unit:** `Restart=on-failure`, `RestartSec=15`. Requires a
  graphical user session for the webview; documented in README.

Restart loop doubles as reconnect logic: openfortivpn exits on tunnel loss → unit
restarts it → webview re-auths silently (or shows login if IdP session expired).

## Error Handling

- Cookie rejected / SAML failure: webview exits non-zero, unit restarts after throttle
  interval; login window appears for the user. No infinite silent loop — each retry is
  visible in the log, and the throttle prevents hammering the gateway.
- Gateway unreachable (no network): openfortivpn exits, unit retries every 15 s until
  network returns.
- Cert mismatch: openfortivpn refuses to connect (fail closed); `vpn status` surfaces
  the last error line from the log.

## Constraints & Caveats

- **FortiClient conflict:** FortiClient must not hold a connection at the same time.
  README instructs to quit FortiClient and disable its login item (uninstall optional).
- **Root:** tunnel creation needs root; scoped sudoers rule keeps it non-interactive
  without granting broad sudo.
- **SAML:** first-ever connect and IdP-session-expiry connects require one interactive
  browser login. No workaround exists by design.

## Testing (manual acceptance)

1. Fresh install via `install.sh` on a clean macOS machine → `vpn status` shows
   connected; internal resource reachable; tunnel IP in `10.212.134.0/24`.
2. Toggle Wi-Fi off/on → tunnel re-establishes within ~30 s without user action.
3. Reboot → tunnel comes up at login without user action (IdP session valid).
4. Expire/clear IdP session → connect shows exactly one login window, then succeeds.
5. `vpn down` → tunnel gone, does not auto-restart until `vpn up` or next login.
6. Repeat 1–3 on Linux (Ubuntu recommended baseline).

## Out of Scope (YAGNI)

- Windows support
- Tray icon / GUI (phase 2 if requested — same plumbing underneath)
- MDM packaging, code signing
- Storing FortiGate credentials (SAML makes them unnecessary)
