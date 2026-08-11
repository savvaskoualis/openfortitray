# OpenFortiTray

A small cross-platform tray client for FortiGate SSL-VPN gateways that authenticate
with SAML/SSO. It keeps the tunnel up, reconnects on its own, connects at login, and
now carries a native Settings window for editing profiles — the things the free
FortiClient tier does not do.

[![CI](https://github.com/savvaskoualis/openfortitray/actions/workflows/ci.yml/badge.svg)](https://github.com/savvaskoualis/openfortitray/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8.svg)](go.mod)

## What it is

OpenFortiTray is the missing convenience layer around a FortiGate SSL-VPN, not a
reimplementation of the client. It does not implement TLS or PPP tunnelling itself,
does not bundle a browser, and does not ask for your password: `openconnect` moves the
packets and your IdP handles the credentials.

- a tray icon with the tunnel state, and Connect / Disconnect
- auto-connect at login (a per-user login item, toggled from the tray or Settings)
- auto-reconnect with exponential backoff (15 s, doubling, capped at 2 min)
- SAML login in your system browser — silent while your IdP session is alive, so
  reconnects after the first login usually need no interaction
- a native **Settings** window (built on [Fyne](https://fyne.io)) with multiple
  connection profiles and Basic/Advanced fields — no JSON editing needed
- one binary per platform whose only runtime dependency is `openconnect`

It sits in the `openforti*` family — `openconnect`, `openfortivpn`, `openfortigui`
and friends — as the piece that turns `openconnect --protocol=fortinet` into a
set-and-forget menu-bar app. The tunnel itself is `openconnect`; OpenFortiTray drives
the SAML handshake, supervises the process, and gives it a UI.

## The Settings window

Open it from the tray: **Settings…**. Everything a connection needs lives here, so a
normal install never touches `config.json` by hand.

<!--
  Add a capture of the Settings window (profile list + Basic/Advanced tabs) at
  docs/screenshot-settings.png and uncomment the line below.
  ![OpenFortiTray Settings window — profile list with the Basic and Advanced tabs](docs/screenshot-settings.png)
-->
_A screenshot of the Settings window belongs at `docs/screenshot-settings.png` — see
the HTML comment above; it is a placeholder until a real capture is dropped in._

- **Profiles.** A list on the left with **Add**, **Duplicate**, **Delete** and
  **Set active**. The active profile is marked `●`; it is the one Connect and
  auto-connect-at-login use.
- **Basic tab.** Profile name, gateway host, an optional custom port (otherwise the
  fixed default `10443`), authentication method, realm, *Auto-connect at login* and
  *Keep VPN up (auto-reconnect)*.
- **Advanced tab.** *Enable IPv6 / dual-stack*, *Prefer DTLS (UDP)*, the
  server-certificate mode (warn / trust / pin, with a fingerprint field when
  pinning), split-DNS domains, the SAML redirect port, the machine-wide `openconnect`
  binary path, and the (read-only) privileged-helper path.
- **Actions.** *Save*, *Save & Reconnect* (tears the tunnel down and brings it back up
  with the new settings), and *Cancel*. A live status strip and Connect / Disconnect
  buttons mirror the tray, so you can drive the tunnel from the window too.

### Connect knows where the gap is

The gateway has no built-in default — it is deployment-specific — so a fresh install
has nothing to dial. Rather than start a doomed tunnel and report a red error, Connect
first asks what would go wrong. If the active profile is incomplete or invalid (no
gateway, a bad port, a not-yet-supported auth method, a pin mode with no fingerprint,
a malformed split-DNS list), it **opens Settings, switches to the offending tab,
focuses and flags the exact field, and raises a banner naming the fix.** No cryptic
connection error, no JSON hunting.

## How it works

One Go binary, `openfortitray`, wires seven internal packages together:

| package | responsibility |
| --- | --- |
| `internal/config` | versioned, multi-profile `config.json` and its defaults/migration |
| `internal/settings` | the Fyne Settings window: profile list, Basic/Advanced tabs, validation, Connect-issue routing |
| `internal/auth` | the SAML external-browser flow, returns an `SVPNCOOKIE` |
| `internal/tunnel` | supervises the `openconnect` process: start, watch, back off, restart |
| `internal/autostart` | per-user login item (LaunchAgent / XDG autostart / scheduled task) |
| `internal/tray` | the menu and status icon (`fyne.io/systray`) |
| `internal/xopen` | opens a path with the OS default handler (`open` / `xdg-open` / `cmd /c start`) |

### The SAML flow

1. OpenFortiTray binds an HTTP listener on `127.0.0.1:8020` (the profile's `saml_port`)
   — the redirect port FortiClient conventionally uses.
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

## Authentication

**SAML / SSO via an external browser is the only method wired into the runtime
today**, and it is the default for every new profile.

The Settings window also *shows* username/password and client-certificate auth, so
the roadmap is visible, but both are **gated**: their sub-fields are disabled, a note
says "not yet supported — use SAML/SSO", and Save refuses to activate a profile that
selects them. The config schema already carries their shape (`auth.method`,
`auth.username`, `auth.cert_path`) so nothing has to change on disk when more auth
methods land later. A password, if ever supported, would go to the OS keychain, never
to `config.json`.

## Requirements

- **openconnect**, with Fortinet protocol support. The installers pull it in for you
  (Homebrew on macOS, apt/dnf/pacman on Linux, winget on Windows). `--protocol=fortinet`
  needs openconnect 8.10 or newer; OpenFortiTray's output parsing was verified against
  9.21, so 9.x is the recommended floor.
- **macOS:** [Homebrew](https://brew.sh) (the installer uses it to get openconnect).
- **Linux:** a D-Bus StatusNotifierItem-capable panel for the tray icon, a working
  OpenGL stack for the Fyne window, and one of apt / dnf / pacman.
- **Building from source:** Go 1.22 or newer, plus a C toolchain and GL/X11 headers —
  Fyne renders through cgo + OpenGL on every OS (see [Building from source](#building-from-source)).

Since the move to Fyne the binary is larger — roughly **25 MB** — because Fyne
statically links the GL bindings, a font shaper and the default theme. On macOS the
app ships as **`OpenFortiTray.app`**, a menu-bar (`LSUIElement`) bundle, not a bare
executable.

## Install

### macOS and Linux

From a checkout of this repository:

```sh
OPENFORTITRAY_GATEWAY=vpn.example.com:10443 bash scripts/install.sh
```

`OPENFORTITRAY_GATEWAY` is required on a first install — OpenFortiTray ships with no
default gateway, because the gateway is deployment-specific. The value is written to
your own `config.json`. Re-runs on a machine whose config already names a gateway do
not need it; the installer never rewrites an existing `config.json`.

Run it as your normal user, not under `sudo`: it calls `sudo` where it needs to, and
the sudoers rule has to name you rather than root. It is idempotent — safe to re-run to
upgrade in place.

The installer:

- installs **openconnect** if it is missing (Homebrew / apt / dnf / pacman);
- on **macOS**, builds and installs **`/Applications/OpenFortiTray.app`** (`make app`
  — a real bundle so `LSUIElement=1` is honoured and the status item renders without
  a Dock icon); on **Linux**, installs `/usr/local/bin/openfortitray`;
- installs the root-owned privileged helper and a sudoers rule scoped to it (see
  [Privilege](#privilege));
- writes a minimal `config.json` (migrated to the multi-profile schema on first launch
  — see [Configuration](#configuration)) and verifies the sudoers rule works.

It does not launch the app or create the login item. On Linux:

```sh
/usr/local/bin/openfortitray &
```

On macOS, open **OpenFortiTray** from `/Applications` (or Spotlight). Then open
**Settings…** to review the profile, and enable *Auto-connect at login* from the tray
menu or the Basic tab.

Other environment knobs:

| variable | effect |
| --- | --- |
| `OPENFORTITRAY_GATEWAY` | gateway as `host:port`; written to `config.json` on a first install |
| `OPENFORTITRAY_RELEASE_URL` | Linux only: download and install this prebuilt binary instead of running `make build`. Ignored on macOS, where the `.app` is always built from the checkout. |
| `OPENFORTITRAY_OPENCONNECT` | use this absolute `openconnect` path instead of the one on `PATH` |
| `OPENFORTITRAY_HELPER_DIR` | install the helper somewhere other than `/usr/local/libexec`; recorded as `helper_path` in `config.json` on a first install |

`OPENFORTITRAY_HELPER_DIR=/usr/libexec` is the escape hatch for an Intel Mac, where
Homebrew leaves `/usr/local` user-owned and the install aborts rather than putting a
passwordless-root helper on a path others can write. It moves three things together:
the helper, the sudoers rule that names it, and the `helper_path` the app dials. On a
first install the value is written into `config.json` for you. The installer never
rewrites an existing `config.json`, so if one already names a different `helper_path`
it stops before touching anything and prints both ways to reconcile them — pass
`OPENFORTITRAY_HELPER_DIR` again on the re-run, or edit the key.

#### macOS Gatekeeper

Release artifacts (the binaries and `OpenFortiTray.app.zip`) are unsigned and
un-notarized. Anything you download with a browser carries the quarantine attribute,
and macOS will refuse to run it. Clear it:

```sh
xattr -dr com.apple.quarantine /Applications/OpenFortiTray.app
```

Or right-click the app in Finder and choose **Open** once, which records your consent.
(Running `scripts/install.sh` from a checkout builds the app locally, so the quarantine
attribute never applies to that path.)

### Windows

Download `openfortitray-windows-amd64.exe` from a release (or run `make release` and
take it from `dist\`), put it next to `scripts\install.ps1`, then from an **elevated**
PowerShell:

```powershell
$env:OPENFORTITRAY_GATEWAY = "vpn.example.com:10443"
.\scripts\install.ps1
```

If `OPENFORTITRAY_GATEWAY` is unset the script prompts for it. It installs to
`%ProgramFiles%\openfortitray`, writes `%APPDATA%\openfortitray\config.json`, installs
openconnect via winget if it is missing, and creates a logon scheduled task named
`OpenFortiTray` with **Run with highest privileges**. That task is also the elevation
mechanism — openconnect needs admin for the wintun adapter — so unchecking
*Auto-connect at login* in the tray deletes it, and a later manual launch has to be
elevated by you.

## Before you connect

> **Important:** quit FortiClient — or any other VPN client for this gateway — before
> connecting, and disable its login item (on macOS: System Settings → General → Login
> Items). Only one client can own the tunnel and the routing table at a time. Two
> running at once will fight over your routes, and the failure looks like a flaky
> network rather than a conflict. See also [Running alongside other
> VPNs](#running-alongside-tailscale--other-vpns).

## Usage

The tray menu:

| item | behaviour |
| --- | --- |
| *status* (disabled) | current state; shows the assigned IP when connected, the last error otherwise |
| **Connect** | dial the active profile — or open Settings on the field to fix if it is not ready |
| **Disconnect** | tear the tunnel down and restore routing |
| **Auto-connect at login** | checkbox; installs or removes the per-user login item |
| **Settings…** | open the Settings window |
| **View logs** | open the log file in your default handler |
| **Quit** | disconnect, wait for teardown, exit |

The icon colour tracks the state: grey disconnected, yellow authenticating /
connecting / reconnecting, green connected, red error.

The first connect opens a browser window for the SAML login. Later connects reuse your
IdP session and are usually silent. A single login attempt is allowed five minutes
before it is abandoned. `Error` is a terminal state for a run — Connect is re-enabled
so you can retry.

## Configuration

**The Settings window is the primary way to configure OpenFortiTray.** This section
documents the on-disk `config.json` as a reference for advanced users and for
scripted deployments; you do not need to edit it by hand.

The file is versioned. The current shape is **schema version 2**, a multi-profile
layout. A file with no `schemaVersion` (the original flat, single-connection layout,
which is also what the installers write) is a *legacy* file and is migrated to schema 2
in place the first time the app loads it. Any key you omit takes its default. The file
is created mode `0600` in a `0700` directory, and read at startup only, so restart the
app (or use *Save* in the window) after editing.

Location:

| OS | `config.json` and `openfortitray.log` |
| --- | --- |
| macOS | `~/Library/Application Support/openfortitray/` |
| Linux | `~/.config/openfortitray/` (or `$XDG_CONFIG_HOME/openfortitray/`) |
| Windows | `%APPDATA%\openfortitray\` |

### Top-level keys

| key | default | meaning |
| --- | --- | --- |
| `schemaVersion` | `2` | on-disk format version; written by the app |
| `activeProfile` | `"Default"` | name of the profile Connect and auto-connect use |
| `profiles` | one `"Default"` profile | the list of connection profiles (see below) |
| `openconnect_path` | `"openconnect"` | openconnect binary. Windows only; on macOS/Linux the helper uses the absolute path baked in at install time and this key is ignored. |
| `helper_path` | `"/usr/local/libexec/openfortitray-tunnel"` | the privileged helper. The sudoers rule is scoped to exactly this path, so changing it without reinstalling makes `sudo` prompt and the tunnel fail to start. |
| `autostart` | `true` | connect the active profile as soon as the app starts, and the preference the *Auto-connect at login* checkbox writes |

### Profile fields

| key | default | meaning |
| --- | --- | --- |
| `name` | `"Default"` | profile name; must be unique |
| `gateway` | `""` | FortiGate SSL-VPN host, no scheme/port. **No default** — while it is empty, Connect opens Settings to add one instead of dialling. |
| `port` | `10443` | gateway HTTPS port |
| `custom_port` | `false` | use `port` instead of the fixed default (FortiClient's *EnableCustomPort*) |
| `saml_port` | `8020` | local port for the SAML redirect listener |
| `auth` | `{"method":"saml"}` | auth method + non-secret params. `method` is `saml` (only supported), `password` or `cert` (gated); `username` and `cert_path` are carried but unused. |
| `realm` | `""` | optional FortiGate realm |
| `dual_stack` | `false` | request IPv6 as well as IPv4 (emits nothing; off emits `--disable-ipv6`) |
| `dtls` | `true` | prefer the DTLS/UDP tunnel (off emits `--no-dtls`) |
| `keep_alive` | `false` | gate the supervisor's auto-reconnect |
| `server_cert` | `{"mode":"warn"}` | server-certificate handling: `mode` is `warn` / `trust` / `pin`; `pin` holds the sha256 fingerprint when `mode` is `pin` |
| `split_dns` | `[]` | corp domains to resolve through the VPN (see [coexistence](#running-alongside-tailscale--other-vpns)) |
| `quiet` | `false` | reserved; quieter openconnect output |

A minimal schema-2 config:

```json
{
  "schemaVersion": 2,
  "activeProfile": "Default",
  "profiles": [
    {
      "name": "Default",
      "gateway": "vpn.example.com",
      "port": 10443,
      "auth": { "method": "saml" },
      "dtls": true,
      "server_cert": { "mode": "warn" }
    }
  ],
  "autostart": true
}
```

The installers deliberately write the older two-key form (`{"gateway": …, "port": …}`);
it is upgraded to the shape above on first launch. Note that `autostart` and the OS
login item are two pieces of state: the tray checkbox reflects the login item and keeps
both in step, while `autostart` on its own only decides whether a launched app connects
immediately.

## Advanced / openconnect flags

The Advanced tab shapes the `openconnect` command line through a small, fixed set of
flags:

| Advanced setting | profile key | openconnect flag |
| --- | --- | --- |
| Prefer DTLS (off) | `dtls: false` | `--no-dtls` |
| IPv6 / dual-stack (off) | `dual_stack: false` | `--disable-ipv6` |
| Server certificate → pin | `server_cert.mode: "pin"` | `--servercert <fingerprint>` |
| Server certificate → warn | `server_cert.mode: "warn"` | *(none — system trust; invalid certs fail)* |

`server_cert` mode `trust` has no effect on its own: modern openconnect has no blanket
"accept any invalid certificate" flag, so trust only honours an explicit fingerprint if
one is supplied. Pin to a known `sha256:…` fingerprint instead.

On macOS and Linux these flags **pass through the security-scoped privileged helper's
allowlist**. The helper's `start` subcommand accepts only three vetted flags —
`--no-dtls`, `--disable-ipv6` and `--servercert <fingerprint>` (whose fingerprint is
charset/length-validated) — and rejects anything else before openconnect is reached, so
threading the Advanced toggles through `sudo` does not widen what can run as root. On
Windows the app is already elevated and passes the same flags to openconnect directly.

Split-DNS domains are **captured and validated today, but not yet installed.** The
field feeds a future scoped-resolver install (`/etc/resolver` on macOS and the
equivalent on Linux); until that automation ships, entering domains here records them
without changing name resolution. See below.

## Running alongside Tailscale / other VPNs

You can run OpenFortiTray next to a mesh VPN like Tailscale, with one caveat and one
hard limit:

- **Corporate-domain DNS needs a scoped resolver.** When another VPN or a local
  resolver owns your DNS, queries for internal names may not go to the FortiGate's DNS.
  The **Split-DNS domains** field is where you list the corp domains that should resolve
  through the tunnel. Note the honest limitation above: OpenFortiTray records and
  validates those domains but does **not** install a scoped resolver yet — that
  automation is a known upcoming item. Until then you may need to add the resolver
  entry by hand.
- **Only one FortiGate SSL-VPN session per user.** This is a FortiGate limit, not an
  OpenFortiTray one: quit FortiClient (and disable its login item) before connecting.
  Two clients fighting over the same session and routing table looks like a flaky
  network, not a conflict.

## Troubleshooting

Start with **View logs**. Every auth attempt, backend start and backend exit lands
there, with openconnect's own last lines attached to the failure.

**Connect keeps opening Settings.** The active profile is not ready to dial — the
banner and the flagged field name the exact problem (no gateway, bad port, an
unsupported auth method, a pin mode with no fingerprint, a malformed split-DNS list).
Fix the field and *Save*.

**The sudoers rule is not effective.** This must print nothing and exit 0:

```sh
sudo -n /usr/local/libexec/openfortitray-tunnel stop
```

A password prompt or an error means the rule is missing, names the wrong user (a common
cause: the installer was run under `sudo`), or `helper_path` in your config no longer
matches the path the rule names. Re-run `scripts/install.sh` as your normal user.

**Stuck on `Reconnecting…` forever.** Usually your VPN session died and your
openconnect build words the rejection differently from the fragments OpenFortiTray
matches (`internal/tunnel/tunnel.go`, `authRejectedMarkers`) — so the supervisor keeps
retrying a dead cookie instead of opening a fresh login. Quitting and relaunching gets
you connected. Please
[file an issue](https://github.com/savvaskoualis/openfortitray/issues) with your
`openconnect --version` output and the relevant lines from `openfortitray.log`, so the
marker can be added.

**Port 8020 is already in use.** The SAML listener cannot bind and auth fails
immediately. Free the port or change the profile's SAML redirect port (Advanced tab /
`saml_port`) — but your FortiGate has to allow the new redirect port for the SAML flow.
8020 is the conventional default; changing it without a matching gateway-side change
will break the redirect.

**Routing is broken after a disconnect.** Teardown depends on openconnect exiting
cleanly; if it was killed harder than that, your routes can be left behind. Ask the
helper to clean up:

```sh
sudo /usr/local/libexec/openfortitray-tunnel stop
```

`stop` is idempotent — running it with no tunnel up is a successful no-op — and it
clears the stale `/var/run/openfortitray-openconnect.pid`.

On Windows there is no helper and no clean interrupt: OpenFortiTray hard-kills
openconnect on disconnect (Go cannot send an interrupt to a Windows process), so routes
and the wintun adapter are left as openconnect had them until you reconnect or reboot —
one of which always restores them. This is by design, not a regression.

**openconnect not found, or a warning about its path.** Re-run `scripts/install.sh`,
or point it at a specific binary with `OPENFORTITRAY_OPENCONNECT=/absolute/path`. On a
shared Mac, prefer a root-owned install location; see [Privilege](#privilege).

## Privilege

`openconnect` has to run as root to create the tunnel interface and edit the routing
table, and an unprivileged tray app cannot signal a root process (`kill(2)` returns
`EPERM`), so it could neither tear the tunnel down nor restore your routes.

On macOS and Linux, OpenFortiTray therefore installs a small root-owned helper,
`/usr/local/libexec/openfortitray-tunnel`, with a sudoers rule in
`/etc/sudoers.d/openfortitray` scoped to that one path:

```
<you> ALL=(root) NOPASSWD: /usr/local/libexec/openfortitray-tunnel
```

The helper takes exactly two subcommands, `start <host:port> [flag …]` and `stop`. The
rule is never written for `openconnect` itself, because a `NOPASSWD` rule on
`openconnect` is passwordless root by way of its own `--script=` / `--csd-wrapper=`
options — both of which openconnect runs as root. Two things keep that boundary intact:

- **The gateway is validated** against a strict `host:port` pattern before it is passed
  on, so those options cannot be smuggled through the gateway argument.
- **Every extra flag is checked against an exact allowlist.** `start` permits only
  `--no-dtls`, `--disable-ipv6` and `--servercert <fingerprint>` (with the fingerprint
  itself charset/length-validated); anything else — any other flag, any
  `--script`/`--csd-wrapper`/`-o`, a `--servercert` with no valid fingerprint — is
  rejected before openconnect is reached. None of the three allowlisted flags takes a
  payload openconnect runs, so the Advanced toggles work without opening a
  root-code-execution hole. Arguments arrive as separate argv elements (no shell
  between the app and the helper), and the sudoers match is on the helper path only, so
  widening `start` needed no sudoers change.

The helper also executes an absolute `openconnect` path baked in at install time rather
than resolving one through `PATH`.

**One boundary is not uniformly enforced.** The installer checks that the openconnect
binary and every directory above it are root-owned and not group/other-writable. On
Linux a failure aborts the install. On macOS it warns and continues, because Homebrew
owns its prefix as the installing user by design — meaning anyone who can write there
could replace openconnect and gain root through the sudoers rule. On a single-user Mac
that person is already an admin who can run `sudo` directly, so the rule grants them
nothing new. On a shared Mac, install openconnect somewhere root-owned and pass
`OPENFORTITRAY_OPENCONNECT`. The full reasoning is in the `THREAT MODEL` blocks at the
top of [`scripts/install.sh`](scripts/install.sh) and
[`scripts/openfortitray-tunnel`](scripts/openfortitray-tunnel). Read them before you
widen anything.

On Windows there is no helper: the app itself runs elevated (via the scheduled task
the installer creates) and runs openconnect directly, because the wintun adapter needs
admin.

## Uninstall

Quit the tray app first.

### macOS

```sh
launchctl bootout gui/$(id -u)/io.github.savvaskoualis.openfortitray 2>/dev/null
rm -f ~/Library/LaunchAgents/io.github.savvaskoualis.openfortitray.plist
sudo rm -rf /Applications/OpenFortiTray.app
sudo rm -f /usr/local/libexec/openfortitray-tunnel /etc/sudoers.d/openfortitray
rm -rf ~/Library/Application\ Support/openfortitray
```

### Linux

```sh
rm -f ~/.config/autostart/openfortitray.desktop
sudo rm -f /usr/local/bin/openfortitray /usr/local/libexec/openfortitray-tunnel /etc/sudoers.d/openfortitray
rm -rf ~/.config/openfortitray
```

If you set `OPENFORTITRAY_HELPER_DIR`, remove the helper from there instead.
`/var/run/openfortitray-openconnect.pid` does not survive a reboot; run the helper's
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
make release   # cross-build the host's release binaries into dist/
make app       # macOS only: assemble dist/OpenFortiTray.app
make install   # bash scripts/install.sh on this machine
```

Since the Fyne v2 migration the tray renders through OpenGL/GLFW, so
`cmd/openfortitray` is a **cgo build on every OS** — the old pure cross-compile model
(`CGO_ENABLED=0` for Linux/Windows from any host) is gone. Each OS builds on its own
native toolchain:

- **macOS:** Xcode Command Line Tools (clang + the macOS SDK). Both darwin slices build
  from an Apple Silicon host because the macOS SDK is a fat SDK.
- **Linux:** `gcc` plus GL/X11 dev headers — `libgl1-mesa-dev` and `xorg-dev`.
- **Windows:** a MinGW `gcc`; `-H=windowsgui` suppresses the console window. It cannot
  be cross-built from a non-Windows host without a MinGW cross-toolchain.

Consequently a local `make release` builds only what the current host's toolchain
supports (both darwin slices on macOS, the linux binary on Linux, the windows exe on
Windows) and says so; the full three-OS set is produced by CI.

CI (`.github/workflows/ci.yml`) runs the full `gofmt` / `go vet` / `go test -race` /
`go build` pass on `ubuntu-latest`, `macos-14` and `windows-latest` — one native runner
per OS, because a green Linux build says nothing about the macOS or Windows tray path —
and parse-checks both shell scripts on Linux.

Tray icons: edit the SVG masters in `assets/icons/` and re-render the embedded PNGs.
See [`assets/README.md`](assets/README.md).

### Releasing

Push a `v*` tag. `.github/workflows/release.yml` builds the matrix on native runners
(`CGO_ENABLED=1` everywhere), verifies the darwin architectures, bundles and zips
`OpenFortiTray.app`, generates `SHA256SUMS` on the runner that produced each artifact,
verifies the round trip, and publishes a GitHub release with generated notes:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The release carries the four binaries (`openfortitray-darwin-arm64`,
`openfortitray-darwin-amd64`, `openfortitray-linux-amd64`,
`openfortitray-windows-amd64.exe`), the macOS `openfortitray-darwin-arm64.app.zip`, and
`SHA256SUMS`. Binaries carry no version stamp yet, and are neither signed nor notarized.

## Contributing

One rule dominates the UI code: **Fyne owns the main thread.** Every mutation of a Fyne
object must happen on the UI goroutine. Cross-goroutine mutations go through
`fyne.Do(...)`, and there is exactly **one event pump** (`app.pump` in
`cmd/openfortitray/main.go`) that reads tunnel events and marshals them onto that
goroutine — the tray and the Settings window are both fed from there. Menu Actions and
widget callbacks already run on the UI goroutine and mutate directly; anything reached
from a supervisor goroutine must not. The app declares the `fyneDo` migration in its
metadata so Fyne's thread-safety checks stay active without the standing advisory.

## License

MIT. See [LICENSE](LICENSE).
</content>
</invoke>
