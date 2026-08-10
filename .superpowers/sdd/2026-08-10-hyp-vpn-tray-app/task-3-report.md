# Task 3 report: tunnel supervisor

- **Worktree branch:** `worktree-agent-a162e7ac74dc991b8` (worktree at `/Users/savvask/code/hyp-vpn/.claude/worktrees/agent-a162e7ac74dc991b8`)
- **Commits:**
  - `2291e42` — `feat: tunnel supervisor with backoff reconnect and openconnect runner`
  - (this report committed separately as `docs:` — see `git log` on the branch)
- **Files added:**
  - `internal/tunnel/tunnel.go`
  - `internal/tunnel/tunnel_test.go`

## What was done

Followed the brief's steps in order (TDD):

1. **Step 1** — wrote `internal/tunnel/tunnel_test.go` first (11 tests).
2. **Step 2** — `go test ./internal/tunnel/` → build failure (`undefined: Event`, `undefined: State`, `undefined: New`, …), i.e. red as expected.
3. **Step 3** — implemented `internal/tunnel/tunnel.go`: `State`/`String()`, `Event`, `ErrAuthRejected`, `Supervisor` + `New`/`Connect`/`Disconnect`, the supervision loop, and `RunOpenconnect`.
4. **Step 4** — `go test ./internal/tunnel/` → 11 passed.
5. **Step 5** — `go vet ./...` clean, `go test -race ./...` → 18 passed across 3 packages, plus `go test -race -count=5 ./internal/tunnel/` → 55 passed (no flakes).
6. **Step 6** — committed `internal/tunnel/` with the brief's message (plus the required `Co-Authored-By` trailer).

## Test commands and output

```
$ go test ./internal/tunnel/          # before implementing
tunnel [build failed]
  internal/tunnel/tunnel_test.go:17:9: undefined: Event
  internal/tunnel/tunnel_test.go:52:48: undefined: State
  internal/tunnel/tunnel_test.go:94:7:  undefined: New
  ... (0 passed, 1 package failed)

$ gofmt -l .
(no output)

$ go test ./internal/tunnel/
Go test: 11 passed in 1 packages

$ go vet ./...
Go vet: No issues found

$ go test -race ./...
Go test: 18 passed in 3 packages

$ go test -race -count=5 ./internal/tunnel/
Go test: 55 passed in 1 packages
```

Tests: `TestStateString`, `TestConnectHappyPath`, `TestConnectIsIdempotent`, `TestReconnectOnDrop`,
`TestAuthRejectedTriggersReauth`, `TestAuthRejectedAfterConnectedReauthsImmediately`,
`TestFreshCookieRejectedBacksOff`, `TestAuthFailureStopsWithError`, `TestDisconnectThenConnectRestarts`,
`TestEmitDoesNotBlockOnFullChannel`, `TestRunOpenconnectStartFailure`. No network access; the only
process spawned is a deliberately nonexistent binary path (start-failure test).

## Deviations from the brief (all deliberate)

1. **`RunOpenconnect` stdout/stderr wiring** — implemented the corrected version per the brief's
   implementer note: a single `io.Pipe`, `cmd.Stdout = pw; cmd.Stderr = pw`, `cmd.Wait()` in a
   goroutine that closes `pw` (giving the scanner EOF) and delivers the wait error over a buffered
   channel, which the caller reads after the scan loop. The brief's `cmd.Stderr = cmd.Stdout` sample
   was not used.
   - Extra safety: `pr.Close()` after the scan loop, so if the scanner stops early (e.g. a line over
     `bufio.Scanner`'s 64 KiB limit) the child's blocked writes fail instead of deadlocking `Wait`.
   - `cmd.Cancel` sends `os.Interrupt` (falling back to `Kill` if signalling fails, e.g. on Windows)
     so openconnect can tear the tunnel down and restore routes, with `cmd.WaitDelay = 10s` as the
     hard backstop. Default `CommandContext` behaviour would SIGKILL.
   - Added one more auth-rejection marker (`"Cookie is no longer valid"`) alongside the brief's three,
     and moved the markers into a slice.

2. **Race-free test collector** — the brief's `collect`/`waitFor` helpers append to and read a shared
   `[]Event` from two goroutines with no synchronisation, which `-race` flags as a data race in the
   test itself. Replaced with a mutex-guarded `collector` type exposing `snapshot()` / `waitFor()` /
   `close()` (the latter also joins the drain goroutine, so no goroutine outlives the test). Test
   counters use `atomic.Int32`/`atomic.Bool` for the same reason. The assertions from the brief's four
   tests are all preserved.

3. **Anti-spin guard on `ErrAuthRejected`** — the brief's loop re-authenticates immediately on
   `ErrAuthRejected` unconditionally. If the gateway rejects even a *freshly minted* cookie (misconfig,
   account disabled, clock skew), that is an unbounded tight loop hammering the SAML endpoint and
   popping a browser window every iteration — bad for an unattended login agent. Implemented:
   immediate re-auth when the rejected cookie was *not* fresh (i.e. it was reused, or it had already
   produced a `Connected`); otherwise emit `Reconnecting` and wait out the backoff before minting a
   new cookie. Covered by `TestAuthRejectedAfterConnectedReauthsImmediately` and
   `TestFreshCookieRejectedBacksOff`.

4. **Backoff schedule** — the brief's sample doubles *before* the first wait, so a never-connected
   tunnel waits 30 s first, not the specified 15 s. Implemented: wait `backoff`, then double, capped
   at `backoffMax` (15 s → 30 s → 60 s → 120 s → 120 s …); a successful `connected` callback resets
   it to `backoffBase`. `backoffBase`/`backoffMax` fields keep the brief's test hook.

5. **Loop generation counter** — the brief's `loop` ends with `defer s.Disconnect()`, which would
   cancel a *newer* loop's context if the user hits Connect while the previous loop is still winding
   down (i.e. `Disconnect()` then `Connect()` in quick succession kills the new connection). Replaced
   with `finish(gen)`, which only clears `s.cancel` when the generation still matches. Covered by
   `TestDisconnectThenConnectRestarts`.

6. **`connected` callback hardening** — the "was connected" flag is an `atomic.Bool` and the callback
   suppresses the `Connected` emit once the loop context is done, so a `runFn` that reports "up" from
   its own goroutine (or racily during teardown) cannot race the loop or emit a stale `Connected`.

7. **`State.String()`** — bounds-checked (`State(99)` returns `"State(99)"`) instead of the brief's
   array index, which would panic on an out-of-range value.

8. Added exported `AuthFunc`/`RunFunc` named types for documentation. `New` keeps the brief's literal
   `func(...)` parameter signatures so callers (and Task 2's bound method value) are unaffected.

## Concerns / notes for later tasks

- **`Disconnected` follows `Error`.** As in the brief's sample, the loop's `defer` emits
  `Disconnected` whenever it exits, including right after an auth-failure `Error` event. A UI that
  renders only "latest state" will show `Disconnected` and lose the error text almost immediately.
  Task 4/5 should surface `Error.Detail` as a notification / sticky menu line rather than relying on
  the state label. Changing this would alter the cross-task event contract, so I kept the brief's
  behaviour.
- **`emit` drops events when the channel is full** (brief's design, kept — never block the loop).
  The tray consumer must keep draining; a buffered channel (≥16) plus a handler that never blocks is
  required, or transitions will be silently lost.
- **`RunOpenconnect` is not unit-tested beyond start-failure** (per the brief; manual integration in
  Task 6). Two things to verify there: (a) with `useSudo`, cancellation signals `sudo` — sudo forwards
  SIGINT/SIGTERM to its child in the common configuration, but confirm openconnect actually exits and
  is not orphaned; (b) the `Connected as <ip>` regex matches the real output of the installed
  openconnect version, otherwise the supervisor never leaves `Connecting` (and backoff never resets).
- **A `runFn` that ignores its context would leak the loop goroutine.** `RunOpenconnect` honours it
  (`CommandContext` + `WaitDelay`); any future backend must too.
- Go directive in `go.mod` is `go 1.26.5`; the package is stdlib-only.

---

## Follow-up: review fixes (commit `18da708`, `fix: supervisor loop overlap, re-auth spin, sticky error event`)

All findings relayed by the coordinator were addressed in `internal/tunnel/tunnel.go` (+ tests).

### 1. Overlapping loops on `Disconnect()` → `Connect()` (IMPORTANT)

- `Supervisor` now carries a per-loop `done chan struct{}`. `Connect()` captures the outgoing loop's
  channel as `prev`, installs a fresh `done`, and passes both to `loop`.
- `loop` **waits for `prev` (or ctx cancellation) before authenticating**, so the previous
  openconnect process is fully gone — including its `WaitDelay` grace period — before a new one is
  spawned. `defer close(done)` is registered first, so it runs last (after `finish` and after the
  terminal event emit).
- `emit` is now **generation-aware**: it takes the loop's `gen`, checks `s.gen == gen` while holding
  the mutex, and drops the event otherwise. A winding-down loop can no longer publish `Connected` or
  `Disconnected` for a tunnel it no longer owns. The non-blocking send happens under the mutex, so
  the generation check and the send are atomic (the send can never block, so this cannot deadlock).
- No deadlock risk in the wait: `Connect()` returns early while a loop is running, so `prev` is
  always a loop that has already been cancelled.

### 2. Re-auth spin on the connected-then-rejected path (IMPORTANT)

- Replaced the `fresh` flag with `proven`: a cookie is proven only after it has held a connection for
  at least `minHealthy` (new field, default **30s**, test hook). The first `connected(ip)` timestamp
  is recorded in an `atomic.Int64`.
- Immediate (zero-delay) re-auth on `ErrAuthRejected` now requires `proven && immediateReauths < 1`
  (`maxImmediateReauths = 1`). Otherwise the loop emits `Reconnecting` and waits out the backoff
  before minting a new cookie. `immediateReauths` resets only after a backoff delay has actually been
  paid — deliberately *not* on every healthy session, so the cap is a hard "one fast re-auth per
  backoff", exactly as requested.
- Net effect: a session killed server-side after hours of uptime still re-authenticates instantly
  (good UX, one browser window); a gateway that connects-then-rejects in a tight loop costs one SAML
  window and then backs off 15s → 30s → … → 120s.

### 3. Auth-error path no longer overwritten (IMPORTANT)

- `loop` sets `authFailed` before emitting `Error`, and the deferred terminal emit skips
  `Disconnected` in that case. `Error` is now the last event of a failed run, so a latest-state UI
  keeps the message. All other exits still emit `Disconnected`.

### Minors

- **(7)** `RunOpenconnect` now scans the captured tail for the rejection markers **unconditionally**,
  before looking at the exit status, so an exit-0 rejection maps to `ErrAuthRejected`. Cancellation
  short-circuits first (`ctx.Err() != nil` → return the wait error as-is).
- **(6)** `sc.Err()` is captured after the scan loop and reported (wrapped, with the tail) when the
  process itself exited cleanly — so a `bufio` error surfaces but never masks a real exit reason.

### Test changes (11)

New/updated in `internal/tunnel/tunnel_test.go` — 14 tests total (was 11):

- `TestEventOrderOnConnect` (new): asserts the exact prefix `Authenticating → Connecting → Connected`,
  and that `Disconnected` is the final event after `Disconnect()`.
- `TestDisconnectThenConnectRestarts` (rewritten): holds loop 1 inside `runFn` across
  `Disconnect()`+`Connect()`, then asserts (a) at most **1** backend ran concurrently, via a
  high-water-mark counter inside `runFn`, and (b) **no `Disconnected` event appears after
  `Connected`** — closing finding 1's blind spot.
- `TestAuthRejectedAfterHealthySessionReauthsImmediately` (renamed from
  `…AfterConnectedReauthsImmediately`): now genuinely healthy (`minHealthy = 20ms`, session sleeps
  40ms) before rejection, and asserts the immediate re-auth reconnects.
- `TestShortLivedConnectionRejectedBacksOff` (new): connects then is rejected below `minHealthy` →
  exactly 1 auth call, no fast re-auth.
- `TestImmediateReauthIsCapped` (new): every session is healthy and rejected → exactly 2 auth calls
  (one immediate re-auth, then the backoff stalls further SAML).
- `TestAuthFailureStopsWithError` (updated): asserts `Error` is the **last** event (no trailing
  `Disconnected`).

### Verification

```
$ gofmt -l .
(no output)

$ go vet ./...
Go vet: No issues found

$ go test -race ./internal/tunnel/
Go test: 14 passed in 1 packages

$ go test -race -count=5 ./internal/tunnel/
Go test: 70 passed in 1 packages

$ go test -race ./...
Go test: 21 passed in 3 packages
```

### Remaining notes

- The "no trailing `Disconnected` after `Error`" change means the UI's state machine must treat
  `Error` as a resting state (Connect re-enabled) — the supervisor loop has stopped at that point.
- `minHealthy` (30s) is the only new tunable; it also gates how quickly a genuinely stale cookie is
  refreshed. A tunnel that never stays up 30s is treated as unhealthy and always backs off.
- After one immediate re-auth, the next stale-cookie rejection waits out the current backoff
  (15s at worst-case first step). Intentional trade-off for spin safety.
