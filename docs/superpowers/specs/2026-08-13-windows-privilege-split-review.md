# Windows privilege split — adversarial security review

Reviews `2026-08-13-windows-privilege-split-design.md` before any code lands, under
the project rule that privileged-path changes get an adversarial review (the same
gate the macOS root helper went through).

**Verdict: the chosen approach (Option B) is sound, but the design as written is not
yet safe to implement.** Eight defects are recorded below; six are must-fix, one is a
must-verify-on-hardware, and one is an accepted-and-documented residual risk. The
three open questions are resolved at the end.

## Threat model

State it precisely, because the whole design rests on it.

The machine's interactive user is a local administrator. They can already do
everything the runner does by clicking through UAC. So the runner grants that
*person* no new capability, and a review that stops there would pass the design.

The threat is not the person. It is **code running as that person without
elevation** — malware, a hijacked npm postinstall, a malicious VS Code extension.
Today such code cannot add routes, create a wintun adapter, or write Program Files:
it would have to raise UAC, which is visible and refusable. A passwordless
elevation path removes that gate. Everything the runner is willing to do, that code
can now do silently.

So the security question for every line of the runner is: *does this hand
unelevated code a capability it did not have?* Arbitrary command execution,
arbitrary file write, and arbitrary process termination as administrator are the
three answers that must be "no".

This is the same bargain as the macOS `NOPASSWD` sudoers rule, which is the reason
Option B is the consistent choice. It is not a reason to be laxer than that review
was.

## Findings

### 1. MUST FIX — the runner's log, state, and config directory must be admin-owned

The design says the runner "appends openconnect's output to a log file the GUI
tails" and "writes a small state file", without saying who owns the directory. If
that directory is user-writable (`%LOCALAPPDATA%`, `%APPDATA%`, `%TEMP%` — where
every other file this app writes lives), the design has an **arbitrary
administrator file write**: unelevated code replaces `state.json` with an NTFS
junction or a hardlink to a file it could not otherwise touch, triggers the task,
and the elevated runner clobbers the target. Hardlink-and-junction redirection of
an elevated writer is one of the most reliably exploited Windows elevation
primitives; a non-admin user does not need `SeCreateSymbolicLink` for either.

Everything the runner writes — openconnect's config file, the log, the state file,
the PID/lock file — goes in a directory only administrators can write, created by
the installer with an explicit ACL rather than inherited defaults:

```
%ProgramData%\OpenFortiTray\run\    Administrators: F   SYSTEM: F   Users: RX
```

`%ProgramData%` itself must not be relied on: its default ACL lets any user create
entries and gives `CREATOR OWNER` full control of what they create. The installer
creates the subdirectory and sets the ACL; the runner **verifies** it at startup
(refuse to run if `Users` hold write) rather than assuming the installer got there.

The GUI reads these files unelevated, which the `Users: RX` grant allows. Reading
is all it needs.

### 2. MUST FIX — the runner must pin its own working directory

Task Scheduler sets a launched task's working directory from the task definition,
and an unset `WorkingDirectory` does not reliably yield a safe one. `openconnect.exe`
and the runner both resolve DLLs with the current directory on the search path
under some load paths, so an attacker-controlled CWD plus one unqualified DLL load
is **administrator code execution** — no request-file parsing required.

The task definition sets `WorkingDirectory` to `%ProgramFiles%\openfortitray`, and
the runner additionally `SetCurrentDirectory`s there itself before spawning
anything, for the same belt-and-braces reason the argv is rebuilt rather than
trusted.

### 3. MUST FIX — the request file needs an owner check, delete-on-read, and no plaintext cookie

The design correctly identifies the request file as untrusted. Three things it does
not say:

- **Owner check.** The runner refuses a request file whose owner SID is not the
  runner's own user. This is not optional (see open question 1): a task's
  *execute* permission is a wider grant than its *write* permission, so a second
  standard user on the machine may be able to trigger the task even though they
  cannot rewrite it. Without the owner check they would drive user A's elevated
  token. With it, they can at worst re-trigger a request A already wrote.
- **Delete on read.** The runner reads the file once into memory, deletes it, and
  validates the in-memory copy — never re-reads the path. This closes the
  re-trigger window above and removes a class of TOCTOU between validation and
  use.
- **Cookie at rest.** Today the SVPNCOOKIE reaches the privileged side over a
  pipe and never lands on disk in the user profile. A plaintext request file is a
  regression in exposure for the single most sensitive value in the app. Wrap it
  with DPAPI (`CryptProtectData`, `CurrentUser` scope) — the runner is the same
  user SID, so it decrypts without a shared secret, and any other unelevated
  process reading the file gets ciphertext.

### 4. MUST FIX — argument validation is under-specified in three places

The design points at "the exact allowlist the macOS helper enforces", which is the
right target, but the runner is new code and the details are where this fails:

- **Gateway must be rejected if it starts with `-`.** A gateway of `--script=...`
  is otherwise a positional argument that openconnect reads as an option, which is
  administrator code execution. `scripts/openfortitray-tunnel` already gets this
  right for both the gateway and the `--servercert` fingerprint; the Windows runner
  must carry the same checks, plus host charset and numeric port.
- **Flags compared as whole tokens.** `--no-dtls` and `--disable-ipv6` must match
  exactly, never by prefix — a prefix test admits `--no-dtls-foo` and, worse,
  anything that happens to share a leading substring with an allowlisted flag.
- **`--servercert` value validated as a fingerprint**: non-empty, no leading `-`,
  not path-like, charset `[A-Za-z0-9:+/=._-]`, length-capped. Port the helper's
  checks rather than writing new ones.

The cookie reuses `validateCookie` from `internal/tunnel` **verbatim** — a second
implementation of that charset is a second chance to get it wrong, and its whole
job is preventing a newline from reaching the config file, where `script=` is
administrator code execution.

### 5. MUST FIX — `stop` must verify the process image before terminating it

The runner will record openconnect's PID so it can stop it. A recorded PID is
stale the moment the process exits, and Windows reuses PIDs aggressively. An
elevated "terminate whatever holds this PID" is **arbitrary process termination as
administrator** — enough to kill a security agent, and enough to be a denial of
service against anything on the machine.

Before terminating, the runner queries the target's full image path and refuses
unless it equals the fixed `%ProgramFiles%\openfortitray\openconnect\openconnect.exe`.

### 6. MUST FIX — the GUI's logon task must be rewritten from HIGHEST to LIMITED

An `asInvoker` manifest does not make the GUI unelevated. A process inherits its
parent's token, so a GUI started by the existing `OpenFortiTray` task
(`/RL HIGHEST`) keeps running elevated after the manifest change, and every
existing install has exactly that task. The result would be the worst of both
designs: an elevated GUI *and* a passwordless elevation path, with the GUI writing
state into a directory whose ACL assumed it was not elevated.

The installer must recreate the task with `/RL LIMITED` (`/F` makes it idempotent),
`internal/autostart` must stop passing `/RL HIGHEST`, and the app should detect that
it is running elevated and say so in the log — a mismatch between the two designs is
worth a breadcrumb rather than silence.

### 7. VERIFIED ON HARDWARE — the task's DACL is execute-only, which is what this needs

If unelevated code can *modify* the tunnel task, the design collapses into one step:
rewrite the action to `cmd.exe`, trigger it, get `RunLevel: Highest`. `schtasks`
exposes no way to set a task's security descriptor, so this rested on Windows
defaults rather than on anything the design chose — and defaults are not evidence.

Measured on the Cloud PC (2026-08-13), from a WSL shell at **Medium** integrity with
`BUILTIN\Administrators` present but "used for deny only" — precisely the attacker's
position — against the existing `OpenFortiTray` task, which the installer registered
through the same `schtasks /Create ... /RL HIGHEST` path the tunnel task will use:

| Probe | Result |
|---|---|
| Modify the action (`Set-ScheduledTask`, COM API) | **Access is denied** |
| Self-register a *new* `RunLevel: Highest` task | **Access is denied** |
| Trigger it (`Start-ScheduledTask`) | **Succeeded** — the design requires this |
| Delete it (`Unregister-ScheduledTask`) | **Access is denied** |

```
icacls C:\Windows\System32\Tasks\OpenFortiTray
  AzureAD\<user>:(R)                          <-- read only
  BUILTIN\Administrators:(I)(R,W,D,WDAC,WO)
  NT AUTHORITY\SYSTEM:(I)(R,W,D,WDAC,WO)
```

`schtasks /Change` is not a valid probe: it prompts for the principal's run-as
password and never reaches the access check, which is why the COM API — the path
real malware uses — is what was tested.

Execute-but-not-modify is exactly the permission shape Option B needs, and it held
on the one machine that matters. Two things follow:

- The refused self-registration is worth noting on its own: unelevated code
  *cannot* mint its own `Highest` task, which confirms the threat model above is
  real rather than theoretical. The elevation this design creates is genuinely new
  capability, and our task is the only gate in front of it.
- Re-run these four probes against `OpenFortiTrayTunnel` once the installer
  registers it, rather than inferring from this one. Same registration path is
  strong evidence, not proof.

Also set `MultipleInstances: IgnoreNew` in the task definition, so a flood of
`schtasks /Run` calls cannot spawn a fleet of runners. The existing task already
carries it, so the installer's current command produces it by default.

### 8. ACCEPTED RISK — arbitrary gateway means route control

Nothing in this design stops unelevated code from asking for a tunnel to a gateway
it controls, and a VPN by definition rewrites the machine's routes. That is a
capability unelevated code does not have today.

It is accepted, for the same reason the macOS review accepted it: the only real
mitigation is an administrator-owned gateway allowlist, which makes changing the
gateway an administrative act and breaks the unelevated Settings window this whole
change exists to enable. The design already rejected Option A partly on those
grounds; accepting the residual here is the consistent position, not a second
standard.

It is bounded, and the bounds are worth stating: the request cannot name a program,
cannot write outside an admin-owned directory, and cannot survive a reboot on its
own. The runner is a route-and-adapter capability, not a code-execution one. That
is the line the six must-fixes above exist to hold.

## Resolved open questions

**1. Must the runner refuse a request file not owned by the task's user?**
Yes — mandatory, not conditional on the machine gaining other users. Combined with
a per-user request path (`%LOCALAPPDATA%`, whose default ACL already excludes other
standard users) and delete-on-read. See finding 3.

**2. Should the runner enforce a single concurrent tunnel itself?**
Yes. The GUI's mutex is an unprivileged, attacker-replaceable component, so it is
not a control the privileged side may rely on. The runner holds a lock file in its
own admin-owned directory, and the task carries `MultipleInstances: IgnoreNew` as a
second layer. Concurrent openconnects fighting over routes and the wintun adapter
is also a correctness problem, so this is cheap either way.

**3. Updater: route through the runner, or accept one UAC prompt per update?**
**Accept the UAC prompt.** Routing it through the runner means the runner executes
an installer binary, which is arbitrary administrator code execution unless the
runner can verify that binary itself. We do not code-sign the exe, so verification
would need a pinned public key in the runner and a signed manifest — a
disproportionate amount of new trusted machinery for a rare, user-initiated action.

The prompt is nearly free: `Setup.exe` already carries
`PrivilegesRequired=admin`, so a plain `Start-Process` on it raises UAC by its own
manifest. And the update becomes *simpler* than today's version, because the
relaunch afterwards is an ordinary unelevated `Start-Process` instead of the
`schtasks`-plus-fallback dance that shipped in 0.1.33.

The user consenting to an update is exactly when a consent prompt belongs. It also
matches macOS, where `brew upgrade --cask` needs no privilege at all.

## Implementation gate

Findings 1-6 are requirements on the implementation plan, not review notes to weigh
later. Finding 7 was the check that could have invalidated the whole approach; it
passed on hardware, so implementation is unblocked. Finding 8 goes in the README's
security section, so the bargain is stated where a user can read it rather than only
in a spec.
