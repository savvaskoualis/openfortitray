# IPsec backend — design

**Goal:** OpenFortiTray can connect to a FortiGate gateway configured for IPsec
remote access, alongside the existing SSL-VPN (openconnect) path, on macOS,
Windows, and Linux.

**Status:** design, not implemented. Blocked on one open question (see below)
before implementation starts, and — like the macOS root helper and the Windows
privilege split — the new privileged helper needs an adversarial security review
before any code lands.

**Driver:** devops is migrating the company FortiGate gateway
(`securityhub.hyperio.cloud`) from SSL-VPN to IPsec. Existing SSL-VPN users are
already seeing collateral damage (an internal DNS ACL appears to have been
narrowed to the new IPsec client pool, breaking `*.hyperio.private` and
`*.database.windows.net` resolution for anyone still on SSL-VPN). Timeline is
therefore driven by devops's rollout, not by us.

## Open question — blocks implementation

We do not yet know the gateway's actual IPsec configuration. A probe against
`securityhub.hyperio.cloud` with `ike-scan` (both IKEv1 and IKEv2 main mode)
got **zero response** on UDP 500 and 4500 — either IPsec is not yet exposed at
that hostname, or it is not live yet at all. Needed from devops before the
implementation plan can be finalized:

- IKE version (v1 or v2)
- Auth mode: PSK, certificate, XAuth+PSK, or EAP
- The actual gateway endpoint (may differ from the SSL-VPN hostname/port)
- For XAuth: whether the username matches the existing SAML identity or is a
  separate credential

The design below is written to accommodate any answer to these (see
"Config schema"), so architecture work can proceed without them, but the
helper's exact connection-profile shape cannot be finalized until they're
known.

## Today

`internal/tunnel.Supervisor` drives exactly one backend: `openconnect
--protocol=fortinet`, run through the root-owned privileged helper
`openfortitray-tunnel` (macOS/Linux) or the scheduled-task runner (Windows).
Everything above the supervisor — the tray icon, the status window, retry/
backoff, notifications — reacts only to the `tunnel.Event` stream and has no
knowledge of what is underneath.

## Approaches considered

**A. strongSwan behind the same `Supervisor` interface (chosen).** A new
backend, selected per-profile, implementing the same `Connect`/`Disconnect`/
`Wait` contract and emitting the same `tunnel.Event`s. strongSwan (`swanctl`)
supports IKEv1+XAuth and IKEv2+EAP, PSK or certs — so it works regardless of
which mode devops picked, and nothing above the tunnel layer needs to change.
Available via Homebrew (macOS), native packages (Linux), and a Windows client.

**B. OS-native IKEv2 clients per platform.** No bundled dependency, but
IKEv2-only — if the gateway is IKEv1+XAuth (the common FortiGate remote-access
default), this approach cannot work at all. Also three separate
platform-specific implementations instead of one shared one.

**C. Wait for devops's config before designing.** Cheapest short-term, but
blocks all work; superseded by writing this design to be parameter-agnostic
instead.

Chosen: **A**. It tolerates not yet knowing the IKE version/auth mode, and it
keeps the existing architecture (one `Supervisor` interface, a privileged
helper per OS, config/UI/tray layers already generalized) instead of forking
it.

## Components

| Component | Identity | Responsibility |
|---|---|---|
| `openfortitray` (GUI) | normal user | tray, settings, backend selection, event display |
| `internal/tunnel` | normal user | `Supervisor` interface; picks SSL or IPsec backend per profile |
| `internal/tunnel` (SSL backend) | — | unchanged: drives `openfortitray-tunnel` |
| `internal/tunnel` (IPsec backend, new) | normal user | drives the new `openfortitray-ipsec` helper via `swanctl` |
| `openfortitray-ipsec` (new helper) | root, via sudoers/scheduled-task rule | validates input, manages `charon`, loads/initiates the connection |

## Backend selection

`tunnel.Supervisor` gains a factory that picks the concrete driver from
`Profile.Backend` (`"ssl"`, the default, or `"ipsec"`) before dialing. Both
drivers emit the same `tunnel.Event` states (`Connecting` / `Connected` /
`Reconnecting` / `Error` / `Disconnected`), so the tray icon, status window,
retry/backoff logic, and notifications need no changes.

## Config schema

`schemaVersion` bumps to 4; `migrate` backfills `Backend: "ssl"` on every
existing profile so current SSL-VPN setups are unaffected.

New fields on `Profile`, populated only when `Backend == "ipsec"`:

- `IKEVersion` (`1` or `2`) — a real field, not a hardcoded assumption, since
  we don't yet know which devops picked.
- Auth reuses the **existing** `AuthConfig`/`AuthMethod` enum
  (`AuthPassword`, `AuthCert`) rather than inventing a parallel credential
  model — those methods are already defined in the schema and explicitly
  marked "designed, not yet implemented"; IPsec is what finally activates
  them. `AuthPassword` carries an XAuth username (non-secret); `AuthCert`
  carries a client-cert path.

Secrets follow the existing rule (never in `config.json`): the PSK and any
XAuth password go into `credstore`, under a new key namespace
(`"openfortitray:ipsec:" + gateway`), reusing the existing `Get`/`Set`/
`Delete` seam — no new secret-storage code.

`Gateway`/`Port` stay on `Profile` unchanged; the IKE port (500/4500) is a
backend-internal default, the same way the SSL backend already hardcodes
`--protocol=fortinet`.

## The privileged helper

A **separate** helper, `openfortitray-ipsec`, with its own sudoers rule (or
scheduled task, on Windows) — not an extension of `openfortitray-tunnel`.

Reasoning: `openfortitray-tunnel`'s threat model (exact flag allowlist, single
baked-in binary path, a bearer cookie piped in on stdin, one foreground
process per connection) was built and reviewed around openconnect's specific
shape. strongSwan's is different enough — a config-file-driven connection
definition, a long-lived `charon` daemon rather than one process per connect,
a PSK/cert instead of a single cookie — that bolting it onto the existing
script would either weaken guarantees that have already been reviewed, or
require just as much new validation anyway. Two independently-reasoned-about
helpers are easier to get right than one helper serving two threat models.

Mechanically, `openfortitray-ipsec start` needs to:

1. Ensure `charon` is running.
2. Write a connection definition (gateway, IKE version, auth mode) to a
   root-owned `0600` file — the same pattern `openfortitray-tunnel` uses for
   the SVPNCOOKIE: never on argv (visible in `ps` for the process's whole
   life), never a shell-interpolated string. The PSK/XAuth password arrives
   the same stdin-not-argv way the cookie does today.
3. `swanctl --load-conns`, then `swanctl --initiate`.

`stop` reverses it: `swanctl --terminate`, then `--unload-conns`.

Route/DNS installation on connect uses strongSwan's `leftupdown` hook, which
calls back into the **existing** `dns-set`/`dns-clear` subcommands on
`openfortitray-tunnel` — split-DNS coexistence (Tailscale MagicDNS, etc.)
gets no second implementation.

Validation follows `openfortitray-tunnel`'s existing pattern: every field that
lands in a file strongSwan parses is checked against a strict allowlisted
charset before anything is written — gateway as `host:port`, IKE version as a
bare `1`/`2`, auth mode from a fixed enum. Never free text passed through.

## UI

Profile's Advanced/Auth section gets a `Backend: SSL / IPsec` selector.
Choosing IPsec swaps the auth sub-form from the SAML-only view (today's only
option) to IKE version + the `AuthPassword`/`AuthCert` fields — new
credential-entry UI, since the app currently has no password field anywhere
(SAML is 100% browser-driven).

## Packaging

macOS/Linux: `strongswan` becomes a declared dependency next to `openconnect`
(Homebrew formula, distro package) — same "assumed present, checked at
runtime" treatment `openconnect`/`HelperPath` already get.

Windows: bundle the strongSwan Windows client the same way `openconnect` is
bundled today (MSYS2 DLL closure, CI-verified via `--version`) — same
mechanism, different binary.

## Testing

- New `validate_*` cases for `openfortitray-ipsec`'s own argument handling,
  mirroring `internal/tunnel/helperscript_test.go`'s coverage of
  `openfortitray-tunnel`.
- A config-migration test for `schemaVersion` 4 (existing profiles gain
  `Backend: "ssl"`).
- A `tunnel` factory test proving `Profile.Backend` selects the correct
  driver.
- No behavior change to the SSL backend or its tests.

## Out of scope

- Anything specific to devops's actual IKE parameters until they're known
  (tracked in "Open question" above).
- A generic (non-FortiGate) IPsec/IKEv2 client — this is scoped to the same
  gateway, same as the SSL-VPN backend.
- Changing or removing the SSL-VPN backend; both coexist per-profile.
