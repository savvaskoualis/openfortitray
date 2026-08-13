# Windows privilege split — design

**Goal:** the tray GUI stops running with an administrator token, and no UAC prompt
appears on launch. The tunnel (openconnect + wintun + routes) still needs
administrator rights, so it moves behind a privileged runner.

**Status:** design, not implemented. Needs an adversarial security review before any
code lands (privileged-path changes carry that rule, like the macOS root helper).

## Today

`openfortitray.exe` carries a `requireAdministrator` manifest, so the whole app —
fyne, OpenGL, the SAML browser launch, the updater — runs elevated. Launching it
manually prompts for UAC; at login the scheduled task (`RunLevel: Highest`) starts it
elevated without a prompt. The GUI owns openconnect as a child process and reads its
stdout for progress ("Configured as <ip>") and for classifying failures.

## The decision that shapes everything: which identity runs the tunnel

### Option A — LocalSystem service (rejected)

A Windows service running as LocalSystem, started at boot, accepting commands from
the unelevated GUI over a named pipe.

Rejected because the escalation surface is severe and the mitigations are invasive.
A SYSTEM process taking instructions from an unprivileged client must treat every
input as hostile:

- **Arbitrary gateway = traffic interception.** An unprivileged local user could ask
  the service to bring up a VPN to a host they control and capture the machine's
  routes. Blocking that needs an administrator-controlled gateway allowlist in a
  protected location — which in turn means *changing the gateway becomes an admin
  action*, a real UX regression against the Settings window we already ship.
- **Any option reaching openconnect = SYSTEM code execution** (`--script`,
  `--csd-wrapper`). Needs the same allowlist discipline as the macOS helper, plus
  care that nothing is read from a user-writable path (TOCTOU).
- **Pipe authorisation** has to be got exactly right, or any local user can control
  or tear down another user's tunnel.

It buys real isolation on a shared machine, at the cost of a much larger attack
surface and an admin-gated gateway.

### Option B — the user's own elevated token, via a scheduled task (chosen)

A scheduled task registered at install time (`RunLevel: Highest`, `UserId:` the
installing user, **fixed arguments**) runs `openfortitray-tunnel.exe`, the Windows
analogue of the POSIX helper script. The GUI, now `asInvoker`, asks the task to run.

Chosen because it mirrors the macOS design we already ship and have reviewed: a
scoped passwordless elevation for one fixed program with validated arguments. The
trust boundary is the same principal — the user is an administrator on this machine
and could perform every one of these actions by clicking through UAC themselves — so
the runner grants no capability the user does not already hold. No LocalSystem, no
gateway allowlist, no pipe ACL to get wrong, and the Settings window keeps working
unelevated.

It is honest about what it is: like the macOS sudoers rule, this is a deliberate
passwordless-elevation path. Its safety rests entirely on the runner validating
everything it is told, which is why the validation below is the security boundary
and not a formality.

## Components

| Component | Identity | Responsibility |
|---|---|---|
| `openfortitray.exe` | normal user (`asInvoker`) | tray, settings, SAML browser flow, updater |
| `openfortitray-tunnel.exe` | user's elevated token (task) | validate, run openconnect, routes/wintun |
| Task `OpenFortiTrayTunnel` | registered by the installer | the elevation gate, fixed args |

## Runner contract

Subcommands mirror the POSIX helper so the two stay conceptually paired:
`start`, `stop`, `abi`.

Parameters cannot ride on the task's argv (a task with fixed arguments cannot carry
per-connect values), so the GUI writes a **request file** and the runner reads it.
That file is user-writable, therefore untrusted, therefore:

- **Gateway**: `host:port`, host matched against a strict charset, numeric port.
  Never a URL, never a path.
- **Flags**: only `--no-dtls`, `--disable-ipv6`, `--servercert <fingerprint>` — the
  exact allowlist the macOS helper enforces, rejected before openconnect is started.
  Nothing that can name a program (`--script`, `--csd-wrapper`, `--config`).
- **Cookie**: the existing `validateCookie` charset (no whitespace, no newline, no
  leading `-`), so it cannot introduce a second line anywhere it is written.
- **openconnect path**: fixed, `%ProgramFiles%\openfortitray\openconnect\`, which
  only administrators can write. Never taken from the request.
- The runner writes openconnect's config itself, in a directory it owns, and builds
  the argv itself. The request file supplies values, never syntax.

## Getting progress back to the GUI

The GUI no longer owns openconnect's stdout, which is what it currently reads for
"Configured as <ip>", the handshake timings, and failure classification
(`ErrAuthRejected` / `ErrPermanent`). The runner therefore:

- appends openconnect's output to a log file the GUI tails, and
- writes a small state file (state, assigned IP, last error class) the GUI polls.

This is the largest piece of work: `internal/tunnel` currently classifies failures
from a live pipe, and that logic has to keep working when the same lines arrive from
a file written by another process.

## Consequences

- No UAC prompt, ever, and the GUI holds no admin token.
- `openfortitray-tunnel.exe` becomes the second privileged component in the project,
  with the same review bar as `scripts/openfortitray-tunnel`.
- Install gains a step (register the task); uninstall must remove it.
- The updater changes: it currently relies on the app being elevated to run
  `Setup.exe` silently. An unelevated GUI cannot, so the update either goes through
  the runner or prompts for elevation once — to be decided before implementation.

## Review outcome

Reviewed in `2026-08-13-windows-privilege-split-review.md`. The approach stands;
the design as written above does not, until six defects are fixed. Read the review
before writing any code — it is a requirements document, not commentary. In short:

- The runner's log/state/config directory must be **admin-owned**, or an elevated
  write into a user-writable path is an arbitrary administrator file write.
- The runner must pin its working directory (DLL search order).
- The request file needs an owner check, delete-on-read, and a DPAPI-wrapped cookie.
- Gateway must be rejected if it starts with `-`; flags matched as whole tokens;
  `validateCookie` reused verbatim.
- `stop` must verify the target's image path before terminating a recorded PID.
- The existing `OpenFortiTray` logon task must be recreated with `/RL LIMITED`, or
  `asInvoker` changes nothing (a child inherits its parent's elevated token).

One item must be checked on real hardware *before* implementation, because failing
it kills this approach: whether unelevated code can modify the tunnel task
(`schtasks /Change`). And one risk is accepted and documented rather than fixed: an
arbitrary gateway means route control.

The three open questions are resolved there — owner check: yes, mandatory; single
tunnel: enforced by the runner itself; updater: accept one UAC prompt, which
`Setup.exe` raises via its own manifest anyway.
