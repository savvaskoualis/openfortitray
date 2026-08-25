# IPsec Backend Plumbing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `Backend` choice (`ssl` / `ipsec`) to profiles, so the config
schema and Settings UI are ready for IPsec, and picking IPsec today gives a
truthful "not yet supported" message instead of a broken or silent connect
attempt — without yet building the real strongSwan runtime.

**Architecture:** `config.Profile` gains a `Backend` field, the same shape as
the existing `AuthMethod` enum. The Settings window gets a Backend selector
next to Gateway/Port. No change to `internal/tunnel`, the privileged helper,
or the auth/run wiring in `cmd/openfortitray/main.go` — those are Phase 2,
blocked on devops's actual IKE parameters (see "Out of scope" below and the
spec).

**Tech Stack:** Go, Fyne (existing UI toolkit), no new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-25-ipsec-support-design.md`

**Correction to the spec, discovered while planning:** the spec's "Config
schema" section says `schemaVersion` bumps to 4 with a migration. That is not
needed. Unlike `RememberSession` (a bool, where JSON's zero value `false` is
indistinguishable from "omitted" and collides with the desired default
`true`), `Backend` is a string whose zero value `""` is unambiguously "not
set" — `normalizeProfile` can default it exactly like `Auth.Method` already
is, with no schemaVersion involvement at all. This plan does that instead.

## Global Constraints

- No new third-party dependencies.
- `Backend` reuses the existing `AuthPassword`/`AuthCert` auth methods for
  IPsec's eventual credential shape (per the spec) — this plan does not add
  new auth-method constants, only wires the existing ones' "not yet
  supported" messaging to also account for `Backend`.
- Every step must leave `go build ./...`, `go vet ./...`, and the full test
  suite passing on darwin (this machine). Do not attempt to build the
  windows/linux targets for this plan — it touches no platform-specific
  files.

---

### Task 1: `Backend` field on `config.Profile`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `type Backend string` with constants `BackendSSL Backend = "ssl"`
  and `BackendIPsec Backend = "ipsec"`, exported from package `config`. A new
  field `Profile.Backend Backend` with json tag `backend`. Task 2 reads and
  writes this field.

- [ ] **Step 1: Write the failing test**

  Add to `internal/config/config_test.go`, in the existing `TestMigrate`
  test's `tests` table (it already covers legacy-upgrade, v2-passthrough, and
  v2-backfill cases — add one more case, and add a assertion to two existing
  ones):

  ```go
  {
      name:         "backend defaults to ssl when omitted",
      raw:          `{"schemaVersion":3,"activeProfile":"A","profiles":[{"name":"A","gateway":"a"}]}`,
      wantUpgraded: false,
      check: func(t *testing.T, c *Config) {
          if c.Profiles[0].Backend != BackendSSL {
              t.Errorf("backend = %q, want %q when omitted", c.Profiles[0].Backend, BackendSSL)
          }
      },
  },
  {
      name:         "explicit backend ipsec is preserved",
      raw:          `{"schemaVersion":3,"activeProfile":"A","profiles":[{"name":"A","gateway":"a","backend":"ipsec"}]}`,
      wantUpgraded: false,
      check: func(t *testing.T, c *Config) {
          if c.Profiles[0].Backend != BackendIPsec {
              t.Errorf("backend = %q, want %q when explicitly set", c.Profiles[0].Backend, BackendIPsec)
          }
      },
  },
  ```

  Also add a `TestNewProfileDefaultsToSSLBackend` test in the same file:

  ```go
  func TestNewProfileDefaultsToSSLBackend(t *testing.T) {
      if defaultProfile().Backend != BackendSSL {
          t.Errorf("defaultProfile().Backend = %q, want %q", defaultProfile().Backend, BackendSSL)
      }
  }
  ```

- [ ] **Step 2: Run the tests to verify they fail**

  Run: `go test ./internal/config/... -run 'TestMigrate|TestNewProfileDefaultsToSSLBackend' -v`
  Expected: FAIL — `c.Profiles[0].Backend` and `defaultProfile().Backend` do
  not compile yet (the field does not exist).

- [ ] **Step 3: Add the `Backend` type and field**

  In `internal/config/config.go`, near the `AuthMethod` type definition, add:

  ```go
  // Backend selects which VPN protocol a profile dials. AuthSSL (openconnect,
  // wired into the runtime today) or AuthIPsec (strongSwan — schema only,
  // not yet wired; see internal/tunnel and the IPsec design doc).
  type Backend string

  const (
      // BackendSSL is openconnect's FortiGate SSL-VPN protocol — the only
      // backend actually implemented today.
      BackendSSL Backend = "ssl"
      // BackendIPsec is FortiGate's IPsec remote-access mode. Forward-designed
      // in the schema; connecting with it is refused with a clear message
      // (see internal/settings' updateAuthNote/authNoteText) until the
      // strongSwan runtime exists.
      BackendIPsec Backend = "ipsec"
  )
  ```

  In `Profile`, add the field right after `Auth`:

  ```go
      Auth  AuthConfig `json:"auth"`
      Backend Backend  `json:"backend"`
      Realm string     `json:"realm,omitempty"`
  ```

  In `normalizeProfile`, add the same shape as the `Auth.Method` default:

  ```go
  func normalizeProfile(p *Profile) {
      if p.Port == 0 {
          p.Port = 10443
      }
      if p.SAMLPort == 0 {
          p.SAMLPort = 8020
      }
      if p.Auth.Method == "" {
          p.Auth.Method = AuthSAML
      }
      if p.Backend == "" {
          p.Backend = BackendSSL
      }
      if p.ServerCert.Mode == "" {
          p.ServerCert.Mode = CertWarn
      }
  }
  ```

  In `defaultProfile`, add `Backend: BackendSSL` alongside the other explicit
  defaults (it is not strictly required, since `normalizeProfile` would
  backfill it too, but every other field in `defaultProfile` is explicit and
  `TestNewProfileDefaultsToSSLBackend` checks `defaultProfile()` directly, not
  a normalized copy):

  ```go
  func defaultProfile() Profile {
      return Profile{
          Name:            "Default",
          Gateway:         "",
          Port:            10443,
          SAMLPort:        8020,
          Auth:            AuthConfig{Method: AuthSAML},
          Backend:         BackendSSL,
          DTLS:            true,
          ...
      }
  }
  ```

- [ ] **Step 4: Run the tests to verify they pass**

  Run: `go test ./internal/config/... -run 'TestMigrate|TestNewProfileDefaultsToSSLBackend' -v`
  Expected: PASS

- [ ] **Step 5: Run the full config package suite**

  Run: `go test ./internal/config/... -v`
  Expected: PASS, including every existing `TestMigrate` case — the new field
  must not perturb any existing assertion (none of them checked exact JSON
  round-trip byte-for-byte, so an added field is safe, but confirm anyway).

- [ ] **Step 6: Commit**

  ```bash
  git add internal/config/config.go internal/config/config_test.go
  git commit -m "feat(config): add a Backend field to Profile (ssl/ipsec)"
  ```

---

### Task 2: Settings UI — Backend selector and accurate "not supported" messaging

**Files:**
- Modify: `internal/settings/settings.go`
- Test: `internal/settings/logic_test.go`

**Interfaces:**
- Consumes: `config.Backend`, `config.BackendSSL`, `config.BackendIPsec` from
  Task 1. `Profile.Backend` field.
- Produces: a pure function `authNoteText(backend config.Backend, method
  config.AuthMethod) string`, used by `updateAuthNote` and unit-tested
  directly (no widget tree needed) — mirrors how `authLabel`/`certModeLabel`
  are already pure, tested helpers in this package.

**Step-by-step:**

- [ ] **Step 1: Write the failing test for the pure message function**

  Add to `internal/settings/logic_test.go`:

  ```go
  func TestAuthNoteText(t *testing.T) {
      tests := []struct {
          name    string
          backend config.Backend
          method  config.AuthMethod
          want    string
      }{
          {"ssl + saml is the only wired combination", config.BackendSSL, config.AuthSAML, ""},
          {"ssl + password not yet supported", config.BackendSSL, config.AuthPassword,
              "(username/password auth not yet supported — use SAML/SSO)"},
          {"ssl + cert not yet supported", config.BackendSSL, config.AuthCert,
              "(client-certificate auth not yet supported — use SAML/SSO)"},
          {"ipsec is not yet supported regardless of auth method", config.BackendIPsec, config.AuthPassword,
              "(IPsec is not yet supported)"},
          {"ipsec overrides even a saml auth method", config.BackendIPsec, config.AuthSAML,
              "(IPsec is not yet supported)"},
      }
      for _, tc := range tests {
          t.Run(tc.name, func(t *testing.T) {
              if got := authNoteText(tc.backend, tc.method); got != tc.want {
                  t.Errorf("authNoteText(%v, %v) = %q, want %q", tc.backend, tc.method, got, tc.want)
              }
          })
      }
  }
  ```

- [ ] **Step 2: Run the test to verify it fails**

  Run: `go test ./internal/settings/... -run TestAuthNoteText -v`
  Expected: FAIL — `authNoteText` is undefined.

- [ ] **Step 3: Extract `authNoteText` and rewire `updateAuthNote`**

  In `internal/settings/settings.go`, replace the existing `updateAuthNote`
  (found via `func (c *Controller) updateAuthNote()`) with:

  ```go
  // authNoteText returns the warning text for a backend/auth-method
  // combination, or "" when the combination is the one wired into the runtime
  // (SSL + SAML). Backend takes precedence: an IPsec profile is not yet
  // supported no matter what its Auth.Method says, and telling the user to
  // "use SAML/SSO" — the SSL-backend message — would be actively wrong advice
  // for a gateway that requires IPsec. Pure, so it is testable without a
  // widget tree.
  func authNoteText(backend config.Backend, method config.AuthMethod) string {
      if backend == config.BackendIPsec {
          return "(IPsec is not yet supported)"
      }
      switch method {
      case config.AuthPassword:
          return "(username/password auth not yet supported — use SAML/SSO)"
      case config.AuthCert:
          return "(client-certificate auth not yet supported — use SAML/SSO)"
      default:
          return ""
      }
  }

  func (c *Controller) updateAuthNote() {
      p := c.work.Profiles[c.sel]
      text := authNoteText(p.Backend, p.Auth.Method)
      c.authNote.SetText(text)
      show(c.authNoteRow, text != "")
      c.relayout()
  }
  ```

- [ ] **Step 4: Run the test to verify it passes**

  Run: `go test ./internal/settings/... -run TestAuthNoteText -v`
  Expected: PASS

- [ ] **Step 5: Add the Backend selector widget**

  In `internal/settings/settings.go`, add a field to `Controller` next to
  `authSelect` (find `authSelect   *widget.Select` in the struct):

  ```go
      backendSelect *widget.Select
  ```

  Near the `authSelect` construction (`c.authSelect = widget.NewSelect(authLabels, ...)`),
  add labels/lookup helpers mirroring `authLabels`/`authMethod`/`authLabel`
  (find those three — they live near the top of the auth-building code) and
  the widget itself:

  ```go
  var backendLabels = []string{"SSL VPN", "IPsec"}

  func backendLabel(b config.Backend) string {
      if b == config.BackendIPsec {
          return "IPsec"
      }
      return "SSL VPN"
  }

  func backendFromLabel(label string) config.Backend {
      if label == "IPsec" {
          return config.BackendIPsec
      }
      return config.BackendSSL
  }
  ```

  Then, alongside `c.authSelect = widget.NewSelect(...)`:

  ```go
  c.backendSelect = widget.NewSelect(backendLabels, func(label string) {
      if c.loading {
          return
      }
      c.work.Profiles[c.sel].Backend = backendFromLabel(label)
      c.updateAuthNote()
  })
  ```

- [ ] **Step 6: Wire it into `loadProfile` and the form layout**

  In `loadProfile` (find `c.authSelect.SetSelected(authLabel(p.Auth.Method))`),
  add immediately after it:

  ```go
  c.backendSelect.SetSelected(backendLabel(p.Backend))
  ```

  In the section that lays out the "Connection" group (find
  `c.section("Connection", widget.NewFormItem("Profile name", ...`), add a row
  after Port:

  ```go
  c.section("Connection",
      widget.NewFormItem("Profile name", c.nameEntry),
      widget.NewFormItem("Gateway host", c.gatewayEntry),
      widget.NewFormItem("Port", narrow(c.portEntry, 150)),
      widget.NewFormItem("Protocol", c.backendSelect),
  ),
  ```

- [ ] **Step 7: Build and run the full settings suite**

  Run: `go build ./... && go test ./internal/settings/... -v`
  Expected: all PASS, including `TestCaptureWindowRenders` (it renders the
  whole window; a new form row must not panic it) and `TestValidateAuthGating`
  (unaffected — it only checks `Auth.Method`, which Backend does not change).

- [ ] **Step 8: Run the full repo suite and vet**

  Run: `go vet ./... && go test ./...`
  Expected: all PASS. `gofmt -l .` should print nothing.

- [ ] **Step 9: Commit**

  ```bash
  git add internal/settings/settings.go internal/settings/logic_test.go
  git commit -m "feat(settings): add a Protocol (SSL/IPsec) selector, with accurate messaging"
  ```

---

## Self-Review

**Spec coverage:** This plan implements the spec's "Config schema" section
(minus the unneeded schemaVersion bump — corrected above) and the "UI"
section's Backend selector. It deliberately does NOT implement "The
privileged helper", "Packaging", or the strongSwan-driven auth/run wiring —
those need devops's IKE parameters first (spec's "Open question"), and
attempting them now would mean guessing a shape that might not fit once the
real parameters are known. That is Phase 2, a separate plan, once unblocked.

**Placeholder scan:** No TBDs; every step has literal code and literal
commands.

**Type consistency:** `config.Backend`, `config.BackendSSL`,
`config.BackendIPsec` (Task 1) are used with those exact names in Task 2's
`authNoteText` signature and test table. `Profile.Backend` is the field name
used consistently in both tasks.

## Out of scope (Phase 2, blocked on devops)

- The `openfortitray-ipsec` privileged helper.
- Wiring `Backend == ipsec` to an actual `AuthFunc`/`RunFunc` pair in
  `cmd/openfortitray/main.go` — worth noting for whoever picks this up: the
  existing `authFn`/`runFn` closures passed to `tunnel.New` already read the
  live profile snapshot per call (see `main.go` around the `tunnel.New(authFn,
  runFn, events)` call), so branching on `tp.prof.Backend` inside those
  closures is the natural integration point — no change to `internal/tunnel`'s
  public surface is needed for that branch itself. What IS still open is
  whether IPsec's auth (PSK/XAuth/EAP, negotiated as part of bringing the IKE
  SA up) fits the existing `AuthFunc → cookie string → RunFunc` shape at all,
  since that shape is SVPNCOOKIE-specific — decide this once IKE
  version/auth-mode are known, not before.
- strongSwan packaging declarations (Homebrew formula, Windows bundling).
- Anything specific to `securityhub.hyperio.cloud`'s actual IKE parameters.
