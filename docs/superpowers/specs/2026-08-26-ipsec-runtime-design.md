# IPsec runtime — design

**Goal:** a profile with `Backend: BackendIPsec` actually connects, on all
three desktop platforms, using discrete Settings fields that cover any valid
IKEv2 configuration (PSK or certificate auth, custom IKE/ESP proposals) —
not just one specific gateway's parameters. This replaces the "IPsec isn't
wired in yet" refusal from the earlier plumbing phase with a real
connection.

**Status:** design, not implemented.

## Why not wait for devops's exact gateway parameters

The original plumbing plan deferred the runtime because it assumed the
implementation would need to be tuned to one specific FortiGate's IKE
parameters. It doesn't: IKEv2 is a standard protocol, and every parameter
that would differ per-gateway (PSK vs certificate, IKE/ESP proposals,
identities) is already something a user fills in on their own profile —
the same way SSL's Gateway/Auth fields aren't hardcoded to one server. There
is nothing gateway-specific left to unblock on.

## Architecture

A new `internal/ipsec` package owns the IPsec connection end to end,
independent of `internal/tunnel` (which stays openconnect-specific and
untouched). `internal/ipsec.Supervisor` implements the same shape
`cmd/openfortitray`'s `supervisor` interface already expects:

```go
type supervisor interface {
    Connect()
    Disconnect()
    Wait(ctx context.Context)
    SetKeepAlive(on bool)
}
```

and writes `tunnel.Event` values onto the same `chan tunnel.Event` the app
already owns and reads from (`internal/ipsec` imports `internal/tunnel` only
for this shared `Event` type — `tunnel` does not import `ipsec`; no cycle).
`cmd/openfortitray`'s `startTunnel` picks which supervisor to construct
based on `profile.Backend` — everything downstream (Status window, tray,
notifications, keepalive) needs zero changes, since it only ever sees
`tunnel.Event` values.

Per-platform connection mechanics live inside `internal/ipsec` via build
tags, mirroring the `glass_*.go` pattern:

| File | Platforms | Mechanism |
|---|---|---|
| `internal/ipsec/ipsec.go` | all | `Supervisor` type, `tunnel.Event` translation, `Connect`/`Disconnect`/`Wait`/`SetKeepAlive` |
| `internal/ipsec/strongswan_unix.go` | darwin, linux (`//go:build darwin \|\| linux`) | drives `swanctl` (strongSwan's CLI), talking to the already-running privileged `charon` daemon |
| `internal/ipsec/ipsec_windows.go` | windows | drives the native Windows IKEv2 VPN API via PowerShell (`Add-VpnConnection`/`rasdial`/`Get-VpnConnection`) |

## Privilege model

Unlike SSL's openconnect path (which needs this app's own sudo helper for
privileged network setup), strongSwan's `charon` daemon runs as its own
privileged service, started independently (`brew services start
strongswan` on macOS, systemd on Linux) — the app only ever talks to it
unprivileged through `swanctl`. **No new privileged helper is needed for
macOS/Linux.** If `swanctl` is missing or `charon` isn't running, Connect
surfaces a clear "install/start strongSwan" message through the existing
connect-issue banner path (see Settings UX below) — never a raw exec error.

Windows' native VPN stack is privileged internally the same way; no custom
helper needed there either.

## Config data model

New fields on `config.Profile`, alongside the existing `Backend` field from
the plumbing phase:

```go
// IPsecAuthMethod distinguishes how the IKE peer is authenticated.
type IPsecAuthMethod string

const (
    IPsecAuthPSK  IPsecAuthMethod = "psk"
    IPsecAuthCert IPsecAuthMethod = "cert"
)

// IPsecConfig holds the fields an IPsec (IKEv2-only) profile needs beyond
// the Gateway field it already shares with SSL profiles. Never holds the
// PSK secret itself — that lives in credstore, same as the SSL password.
type IPsecConfig struct {
    AuthMethod  IPsecAuthMethod `json:"authMethod"`
    // LocalID/RemoteID are IKE identities. RemoteID defaults to the
    // profile's Gateway host if left blank.
    LocalID  string `json:"localID,omitempty"`
    RemoteID string `json:"remoteID,omitempty"`
    // CertPath/KeyPath are used only when AuthMethod == IPsecAuthCert.
    CertPath string `json:"certPath,omitempty"`
    KeyPath  string `json:"keyPath,omitempty"`
    // IKEProposal/ESPProposal are strongSwan-style cipher-suite strings
    // (e.g. "aes256-sha256-modp2048"), pre-filled with a strong default
    // and editable — this is what makes "any valid IKEv2 config" possible
    // without a raw config-file paste-in.
    IKEProposal string `json:"ikeProposal,omitempty"`
    ESPProposal string `json:"espProposal,omitempty"`
}
```

`Profile.IPsec IPsecConfig` (json tag `ipsec`), populated only when
`Backend == BackendIPsec`; zero value otherwise, matching how the plumbing
phase already leaves IPsec-only fields dormant for SSL profiles.

**Defaults** (used to pre-fill new profiles and the Advanced fields):
`IKEProposal`/`ESPProposal` both default to `"aes256-sha256-modp2048"` — a
strong, widely-supported modern suite. `LocalID` defaults empty (most IKEv2
setups don't need an explicit local identity beyond the client cert/PSK
lookup). `RemoteID` defaults to the profile's `Gateway` host.

**IKEv2 only** — no version selector. IKEv1 is legacy, and Windows' native
API is IKEv2-only, so locking to IKEv2 everywhere keeps the three platforms
consistent instead of exposing a mode one platform can't do.

**PSK secret storage:** stored via `credstore`, exactly like the SSL
password/cookie today, under a distinct key so it can't collide with an SSL
profile sharing the same `Gateway`: `"openfortitray:ipsec-psk:" + gateway`
(SSL's existing key is `"openfortitray:" + gateway`).

## Settings UX

Reuses existing patterns — no new UI chrome, no new error-handling shape:

- **Basic vs Advanced split** (already exists — `SectionConnection` /
  `SectionAdvanced`): Gateway (shared with SSL) + Auth method (PSK /
  Certificate) + the one secret/cert field the chosen method needs live in
  Connection, at the same level SSL's fields already sit. `IKEProposal`,
  `ESPProposal`, `LocalID`, `RemoteID` move to Advanced, pre-filled with
  defaults — most users never touch them.
- **Progressive disclosure:** picking PSK vs Certificate swaps which field
  shows (secret entry vs cert/key file pickers), the same way Basic vs Cert
  auth already does for SSL profiles today.
- **Cert/key selection:** native file-open dialog (`dialog.ShowFileOpen`),
  not a raw path text box.
- **PSK secret entry:** a secure-entry field backed by `credstore`, visually
  and behaviorally identical to the existing SSL password field.
- **Validation:** extends the existing `validateBackendSupported` /
  `FirstConnectIssue` gate — a missing secret/cert for the selected auth
  method routes to Settings, focuses the field, and shows a banner exactly
  like every other blocking config issue already does (wrong gateway,
  missing profile name, etc.). No new error-UX pattern.
- **"strongSwan not installed / not running"** and **"Windows VPN service
  unavailable"** surface through that same connect-issue banner path.
- The plumbing phase's "IPsec isn't wired in yet" note in
  `internal/settings/logic.go` (`backendNoteText`) is removed once this
  ships — it would no longer be true.

## Connect / Disconnect flow

**macOS/Linux (strongSwan):**
1. Connect writes a scoped `swanctl.conf` connection fragment plus a
   secrets fragment (PSK, or cert/key paths) into strongSwan's config
   directory, named uniquely per profile to avoid clobbering any
   pre-existing strongSwan config on the machine.
2. Runs `swanctl --load-all`, then `swanctl --initiate -c <conn-name>`.
3. Tails `charon`'s log (via the system log on macOS, `journalctl --unit
   strongswan -f` or the configured log file on Linux) for state
   transitions, translating them into `tunnel.Event` values the same way
   `internal/tunnel` already translates openconnect's stdout.
4. Disconnect runs `swanctl --terminate -c <conn-name>` and removes the
   written config/secrets fragments.

**Windows (native IKEv2):**
1. Connect ensures a VPN connection profile exists for this OpenFortiTray
   profile (`Get-VpnConnection` / `Add-VpnConnection`, PSK or
   cert-authentication parameters set accordingly), then brings it up
   (`rasdial <name>` or `Connect-VpnConnection`).
2. Polls `Get-VpnConnection`'s `ConnectionStatus` for state transitions,
   translating them into the same `tunnel.Event` values.
3. Disconnect calls `rasdial <name> /disconnect` (or
   `Disconnect-VpnConnection`).

Both paths map onto the existing `Connecting` / `Connected` /
`Reconnecting` / `Disconnected` event vocabulary — the Status window, tray
icon, and notifications need no changes to understand an IPsec session.

## Error handling / fallback behavior

- Every native call (`swanctl`, `charon` log tail, PowerShell VPN cmdlets)
  is best-effort: a failure is logged and surfaced through the existing
  connect-issue banner, never a crash.
- A missing `swanctl`/`charon` (macOS/Linux) or an unavailable Windows VPN
  service is detected before attempting to connect and produces a specific,
  actionable message (what's missing, not just "connection failed") —
  matching the existing "openconnect not found" handling for SSL.
- KeepAlive (`SetKeepAlive`) behaves identically to the SSL path: on
  unexpected disconnect, the same reconnect/backoff logic in the shared
  `tunnel.Event` consumer applies, since `internal/ipsec` only ever speaks
  that event vocabulary.

## Testing approach

- `internal/ipsec`'s event-translation logic (charon log lines /
  `Get-VpnConnection` states → `tunnel.Event`) is unit-testable the same way
  `internal/tunnel`'s openconnect log parsing already is: table-driven tests
  against captured real log/output samples, no live strongSwan/Windows VPN
  service required.
- The actual `swanctl`/PowerShell invocations cannot be exercised in CI or
  on this dev machine for Windows; behavioral verification for each
  platform happens on real hardware, same honesty standard the glass UI
  plan already established (state clearly what was verified vs. what
  wasn't, per platform).
- macOS is the one platform this can be behaviorally tested on this
  machine, given strongSwan is installable via Homebrew here.

## Out of scope

- IKEv1 — legacy, and inconsistent with Windows' IKEv2-only native API.
- A raw config-file paste-in mode — discrete fields already cover "any
  valid IKEv2 config" without the validation and Windows-compatibility
  problems a free-text blob would introduce.
- Split-tunnel / routing customization beyond what the gateway's IKE
  Configuration Payload (CP) already provides — matches how the SSL path
  already just takes whatever routes openconnect reports, no user-facing
  routing UI.
- Certificate *generation* or CA management — the cert/key file pickers
  assume the user already has valid files from their PKI; this app doesn't
  become a certificate authority.
