# OpenFortiTray

A small cross-platform tray client for FortiGate SSL-VPN gateways that authenticate
with SAML/SSO. It keeps the tunnel up, reconnects on its own, and connects at login —
the things the free FortiClient tier does not do.

[![CI](https://github.com/savvaskoualis/openfortitray/actions/workflows/ci.yml/badge.svg)](https://github.com/savvaskoualis/openfortitray/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8.svg)](go.mod)

## Why

FortiClient's free tier has no auto-connect: every session starts with opening the
app and clicking Connect, and a dropped link stays dropped until somebody notices.
OpenFortiTray is the missing piece, not a reimplementation of the client:

- a tray icon with the tunnel state, and Connect / Disconnect
- auto-connect at login (a per-user login item, toggled from the tray)
- auto-reconnect with exponential backoff (15 s, doubling, capped at 2 min)
- SAML login in your system browser — silent while your IdP session is alive, so
  reconnects after the first login usually need no interaction
- one static binary per platform, no runtime dependencies beyond `openconnect`

It does not implement TLS or PPP tunnelling itself, does not bundle a browser, and
does not ask for your password: `openconnect` moves the packets and your IdP handles
the credentials.

## How it works

One Go binary, `openfortitray`, wires six internal packages together:

| package | responsibility |
| --- | --- |
| `internal/config` | `config.json` and defaults |
| `internal/auth` | the SAML external-browser flow, returns an `SVPNCOOKIE` |
| `internal/tunnel` | supervises the `openconnect` process: start, watch, back off, restart |
| `internal/autostart` | per-user login item (LaunchAgent / XDG autostart / scheduled task) |
| `internal/tray` | the menu and status icon (`fyne.io/systray`) |
| `internal/xopen` | opens a path with the OS default handler (`open` / `xdg-open` / `cmd /c start`) |

### The SAML flow

1. OpenFortiTray binds an HTTP listener on `127.0.0.1:8020` (`saml_port`) — the redirect
   port FortiClient conventionally uses.
2. It opens `https://<gateway>:<port>/remote/saml/start?redirect=1` in your default
   browser.
3. You authenticate at the IdP. If your IdP session is still valid this needs no
   interaction and takes a second or two.
4. The FortiGate redirects the browser to `http://127.0.0.1:8020/?id=<auth-id>`.
5. OpenFortiTray exchanges that id at `/remote/saml/auth_id?id=…` for the `SVPNCOOKIE`
   session cookie, and the tab reports that you can close it.

### The tunnel

The cookie is handed to `openconnect --protocol=fortinet --cookie-on-stdin
--non-inter <host:port>` on stdin, so it never appears in the process table. The
supervisor scans openconnect's merged output for the assigned address
(`Configured as <ip>`) and for the log fragments that mean the gateway refused the
cookie — the latter is what turns a dead session into a fresh browser login instead
of an endless retry loop.

### Privilege

`openconnect` has to run as root to create the tunnel interface and edit the routing
table, and an unprivileged tray app cannot signal a root process (`kill(2)` returns
`EPERM`), so it could neither tear the tunnel down nor restore your routes.

On macOS and Linux, OpenFortiTray therefore installs a small root-owned helper,
`/usr/local/libexec/openfortitray-tunnel`, with a sudoers rule in `/etc/sudoers.d/openfortitray`
scoped to that one path:

```
<you> ALL=(root) NOPASSWD: /usr/local/libexec/openfortitray-tunnel
```

The helper takes exactly two subcommands, `start <host:port>` and `stop`. The rule is
never written for `openconnect` itself, because a `NOPASSWD` rule on `openconnect` is
passwordless root by way of its own `--script=` / `--csd-wrapper=` options — both of
which openconnect runs as root. The helper validates its gateway argument against a
strict `host:port` pattern before passing it on, so those options cannot be smuggled
through, and it executes an absolute `openconnect` path baked in at install time
rather than resolving one through `PATH`.

**One boundary is not uniformly enforced.** The installer checks that the openconnect
binary and every directory above it are root-owned and not group/other-writable. On
Linux a failure aborts the install. On macOS it warns and continues, because Homebrew
owns its prefix as the installing user by design — meaning anyone who can write there
could replace openconnect and gain root through the sudoers rule. On a single-user Mac
that person is already an admin who can run `sudo` directly, so the rule grants them
nothing new. On a shared Mac, install openconnect somewhere root-owned and pass
`OPENFORTITRAY_OPENCONNECT`. The full reasoning is in the `THREAT MODEL` blocks at the top of
[`scripts/install.sh`](scripts/install.sh#L19-L35) and
[`scripts/openfortitray-tunnel`](scripts/openfortitray-tunnel#L21-L41). Read them before you widen
anything.

On Windows there is no helper: the app itself runs elevated (via the scheduled task
the installer creates) and runs openconnect directly, because the wintun adapter needs
admin.

## Requirements

- **openconnect**, with Fortinet protocol support. The installers pull it in for you
  (Homebrew on macOS, apt/dnf/pacman on Linux, winget on Windows). `--protocol=fortinet`
  needs openconnect 8.10 or newer; OpenFortiTray's output parsing was verified against 9.21,
  so 9.x is the recommended floor.
- **macOS:** [Homebrew](https://brew.sh) (the installer uses it to get openconnect).
- **Linux:** a D-Bus StatusNotifierItem-capable panel for the tray icon, and one of
  apt / dnf / pacman.
- **Building from source:** Go 1.22 or newer. Not needed if you install a prebuilt
  binary.

## Install

### macOS and Linux

From a checkout of this repository:

```sh
OPENFORTITRAY_GATEWAY=vpn.example.com:10443 bash scripts/install.sh
```

`OPENFORTITRAY_GATEWAY` is required on a first install — OpenFortiTray ships with no default
gateway, because the gateway is deployment-specific. The value is written to your own
`config.json`. Re-runs on a machine whose config already names a gateway do not need
it; the installer never rewrites an existing `config.json`.

Run it as your normal user, not under `sudo`: it calls `sudo` where it needs to, and
the sudoers rule has to name you rather than root. It is idempotent — safe to re-run to
upgrade in place.

To install a prebuilt binary instead of building from the checkout:

```sh
OPENFORTITRAY_GATEWAY=vpn.example.com:10443 \
OPENFORTITRAY_RELEASE_URL=https://github.com/savvaskoualis/openfortitray/releases/download/v0.1.0/openfortitray-darwin-arm64 \
  bash scripts/install.sh
```

You still need the checkout (or at least `scripts/`) either way: the privileged helper
is installed from `scripts/openfortitray-tunnel`.

Other environment knobs:

| variable | effect |
| --- | --- |
| `OPENFORTITRAY_GATEWAY` | gateway as `host:port`; written to `config.json` on a first install |
| `OPENFORTITRAY_RELEASE_URL` | download and install this binary instead of running `make build` |
| `OPENFORTITRAY_OPENCONNECT` | use this absolute `openconnect` path instead of the one on `PATH` |
| `OPENFORTITRAY_HELPER_DIR` | install the helper somewhere other than `/usr/local/libexec`; recorded as `helper_path` when the installer writes `config.json` |

The installer places `/usr/local/bin/openfortitray`, the helper, and the sudoers rule, then
verifies that `sudo -n <helper> stop` runs without prompting — using the `helper_path`
your `config.json` actually names, not just the path it installed to. It does not launch
the app or create the login item:

```sh
/usr/local/bin/openfortitray &
```

Enable auto-connect at login from the tray menu afterwards.

`OPENFORTITRAY_HELPER_DIR=/usr/libexec` is the escape hatch for an Intel Mac, where
Homebrew leaves `/usr/local` user-owned and the install aborts rather than putting a
passwordless-root helper on a path others can write. It moves three things together:
the helper, the sudoers rule that names it, and the `helper_path` the app dials. On a
first install the value is written into `config.json` for you. The installer never
rewrites an existing `config.json`, so if one already names a different `helper_path`
it stops before touching anything and prints both ways to reconcile them — pass
`OPENFORTITRAY_HELPER_DIR` again on the re-run, or edit the key.

#### macOS Gatekeeper

Release binaries are unsigned and un-notarized. A binary you downloaded with a browser
carries the quarantine attribute, and macOS will refuse to run it:

```sh
xattr -d com.apple.quarantine ~/Downloads/openfortitray-darwin-arm64
```

Or right-click the binary in Finder and choose **Open** once, which records your
consent. `curl` does not set the attribute, so the `OPENFORTITRAY_RELEASE_URL` path above is
unaffected.

### Windows

Download `openfortitray-windows-amd64.exe` from a release (or run `make release` and take it
from `dist\`), put it next to `scripts\install.ps1`, then from an **elevated**
PowerShell:

```powershell
$env:OPENFORTITRAY_GATEWAY = "vpn.example.com:10443"
.\scripts\install.ps1
```

If `OPENFORTITRAY_GATEWAY` is unset the script prompts for it. It installs to
`%ProgramFiles%\openfortitray`, writes `%APPDATA%\openfortitray\config.json`, installs openconnect
via winget if it is missing, and creates a logon scheduled task named `OpenFortiTray` with
**Run with highest privileges**. That task is also the elevation mechanism — openconnect
needs admin for the wintun adapter — so unchecking *Auto-connect at login* in the tray
deletes it, and a later manual launch has to be elevated by you.

## Before you connect

> **Important:** quit FortiClient — or any other VPN client for this gateway — before
> connecting, and disable its login item (on macOS: System Settings → General → Login
> Items). Only one client can own the tunnel and the routing table at a time. Two
> running at once will fight over your routes, and the failure looks like a flaky
> network rather than a conflict.

## Usage

The tray menu:

| item | behaviour |
| --- | --- |
| *status* (disabled) | current state; shows the assigned IP when connected, the last error otherwise |
| **Connect** | authenticate if needed, then bring the tunnel up |
| **Disconnect** | tear the tunnel down and restore routing |
| **Auto-connect at login** | checkbox; installs or removes the per-user login item |
| **View logs** | open the log file in your default handler |
| **Quit** | disconnect, wait for teardown, exit |

The icon colour tracks the state: grey disconnected, yellow authenticating /
connecting / reconnecting, green connected, red error.

The first connect opens a browser window for the SAML login. Later connects reuse your
IdP session and are usually silent. A single login attempt is allowed five minutes
before it is abandoned. `Error` is a terminal state for a run — Connect is re-enabled
so you can retry.

### Configuration

| OS | `config.json` and `openfortitray.log` |
| --- | --- |
| macOS | `~/Library/Application Support/openfortitray/` |
| Linux | `~/.config/openfortitray/` (or `$XDG_CONFIG_HOME/openfortitray/`) |
| Windows | `%APPDATA%\openfortitray\` |

The file is optional: any key you omit takes its default. It is created mode `0600` in
a `0700` directory. OpenFortiTray reads it at startup only, so restart the app after editing.

| key | default | meaning |
| --- | --- | --- |
| `gateway` | `""` | FortiGate SSL-VPN host, no port. **No default** — without it the tray reports `gateway not set` and refuses to dial. |
| `port` | `10443` | gateway HTTPS port |
| `saml_port` | `8020` | local port for the SAML redirect listener |
| `openconnect_path` | `"openconnect"` | openconnect binary. Windows only; on macOS/Linux the helper uses the absolute path baked in at install time and this key is ignored. |
| `helper_path` | `"/usr/local/libexec/openfortitray-tunnel"` | the privileged helper. The sudoers rule is scoped to exactly this path, so changing it without reinstalling makes `sudo` prompt and the tunnel fail to start. |
| `autostart` | `true` | connect as soon as the app starts, and the preference the tray checkbox writes |

A minimal config:

```json
{
  "gateway": "vpn.example.com",
  "port": 10443
}
```

Note that `autostart` and the OS login item are two pieces of state. The tray checkbox
reflects the login item and keeps both in step; `autostart` on its own only decides
whether a launched app connects immediately.

## Troubleshooting

Start with **View logs**. Every auth attempt, backend start and backend exit lands
there, with openconnect's own last lines attached to the failure.

**The sudoers rule is not effective.** This must print nothing and exit 0:

```sh
sudo -n /usr/local/libexec/openfortitray-tunnel stop
```

A password prompt or an error means the rule is missing, names the wrong user (a common
cause: the installer was run under `sudo`), or `helper_path` in your config no longer
matches the path the rule names. Re-run `scripts/install.sh` as your normal user.

**Status reads `gateway not set — see config.json`.** No gateway is configured. Add one
to `config.json` (see the table above) and restart the app; the log file names the exact
path.

**Stuck on `Reconnecting…` forever.** Usually your VPN session died and your
openconnect build words the rejection differently from the fragments OpenFortiTray matches
(`internal/tunnel/tunnel.go`, `authRejectedMarkers`) — so the supervisor keeps retrying
a dead cookie instead of opening a fresh login. Quitting and relaunching gets you
connected. Please
[file an issue](https://github.com/savvaskoualis/openfortitray/issues) with your
`openconnect --version` output and the relevant lines from `openfortitray.log`, so the marker
can be added.

**Port 8020 is already in use.** The SAML listener cannot bind and auth fails
immediately. Free the port or set `saml_port` to something else — but your FortiGate
has to allow the new redirect port for the SAML flow. 8020 is the conventional default;
changing it without a matching gateway-side change will break the redirect.

**Routing is broken after a disconnect.** Teardown depends on openconnect exiting
cleanly; if it was killed harder than that, your routes can be left behind. Ask the
helper to clean up:

```sh
sudo /usr/local/libexec/openfortitray-tunnel stop
```

`stop` is idempotent — running it with no tunnel up is a successful no-op — and it
clears the stale `/var/run/openfortitray-openconnect.pid`.

On Windows there is no helper and no clean interrupt: OpenFortiTray hard-kills openconnect on
disconnect (Go cannot send an interrupt to a Windows process), so routes and the wintun
adapter are left as openconnect had them until you reconnect or reboot — one of which
always restores them. This is by design, not a regression.

**openconnect not found, or a warning about its path.** Re-run `scripts/install.sh`,
or point it at a specific binary with `OPENFORTITRAY_OPENCONNECT=/absolute/path`. On a shared
Mac, prefer a root-owned install location; see [Privilege](#privilege).

## Uninstall

### macOS

```sh
launchctl bootout gui/$(id -u)/io.github.savvaskoualis.openfortitray 2>/dev/null
rm -f ~/Library/LaunchAgents/io.github.savvaskoualis.openfortitray.plist
sudo rm -f /usr/local/bin/openfortitray /usr/local/libexec/openfortitray-tunnel /etc/sudoers.d/openfortitray
rm -rf ~/Library/Application\ Support/openfortitray
```

### Linux

```sh
rm -f ~/.config/autostart/openfortitray.desktop
sudo rm -f /usr/local/bin/openfortitray /usr/local/libexec/openfortitray-tunnel /etc/sudoers.d/openfortitray
rm -rf ~/.config/openfortitray
```

Quit the tray app first. If you set `OPENFORTITRAY_HELPER_DIR`, remove the helper from there
instead. `/var/run/openfortitray-openconnect.pid` does not survive a reboot; run the helper's
`stop` once before deleting it to clear the file on a live system.

### Windows

From an elevated PowerShell:

```powershell
schtasks /Delete /TN "OpenFortiTray" /F
Remove-Item -Recurse -Force "$env:ProgramFiles\openfortitray"
Remove-Item -Recurse -Force "$env:APPDATA\openfortitray"
```

openconnect was installed as a separate package; remove it with
`winget uninstall OpenConnect.OpenConnect` if you no longer want it.

## Building from source

```sh
make build     # go build -o openfortitray ./cmd/openfortitray
make test      # go vet ./... && go test -race ./...
make release   # cross-build all four release binaries into dist/
make install   # bash scripts/install.sh on this machine
```

`make release` produces `openfortitray-darwin-arm64`, `openfortitray-darwin-amd64`,
`openfortitray-linux-amd64` and `openfortitray-windows-amd64.exe`. The darwin slices need cgo
(`fyne.io/systray` drives the Cocoa status bar) and therefore a macOS host; linux and
windows cross-build with `CGO_ENABLED=0`.

CI runs the test target on `ubuntu-latest` and `macos-14`, checks `gofmt`, and
parse-checks both shell scripts. The tray layer is the platform-specific part, so a
green Linux build says nothing about the macOS path — hence two runners.

Tray icons: edit the SVG masters in `assets/icons/` and re-render the embedded PNGs.
See [`assets/README.md`](assets/README.md).

### Releasing

Push a `v*` tag. `.github/workflows/release.yml` cross-builds all four binaries,
verifies the darwin architectures, generates `SHA256SUMS`, and publishes a GitHub
release with generated notes:

```sh
git tag v0.1.0
git push origin v0.1.0
```

Release binaries carry no version stamp yet, and are neither signed nor notarized.

## License

MIT. See [LICENSE](LICENSE).
