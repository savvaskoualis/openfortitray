package tunnel

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helperScript returns the path to the repo's privileged helper. It is exercised
// here, from the Go tests, because the sudoers rule installed by
// scripts/install.sh grants passwordless root for this script: its argument
// validation is the only thing standing between a local caller and root code
// execution via openconnect's --script/--csd-wrapper options, so it needs
// coverage that runs on every `go test`.
func helperScript(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "scripts", "openfortitray-tunnel"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("privileged helper missing from the repo: %v", err)
	}
	return p
}

// runHelper runs the helper under /bin/sh (never as root) and returns its
// combined output and exit code.
func runHelper(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{helperScript(t)}, args...)...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running helper: %v", err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

// readHelperSource returns the repo helper's text.
func readHelperSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(helperScript(t))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// helperStopTimeoutSeconds parses STOP_TIMEOUT from the helper — the window,
// after SIGINT, in which openconnect sends its FortiGate logout and exits.
func helperStopTimeoutSeconds(t *testing.T) int {
	t.Helper()
	m := regexp.MustCompile(`(?m)^STOP_TIMEOUT=(\d+)`).FindStringSubmatch(readHelperSource(t))
	if m == nil {
		t.Fatal("could not find STOP_TIMEOUT in the helper")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Teardown exists to give openconnect time to log out of the FortiGate on SIGINT
// so no session lingers server-side to reject the next run's first cookie. Every
// bound in the ladder must sit OUTSIDE the one it wraps, or a stop is killed
// mid-logout. This pins the constants against a regression that would shrink the
// window.
func TestTeardownLogoutTimingInvariants(t *testing.T) {
	stop := time.Duration(helperStopTimeoutSeconds(t)) * time.Second

	// The Go-side bound on one privileged stop call must outlast the helper's own
	// SIGINT-and-wait window, or the call is cancelled before openconnect finishes
	// its logout.
	if helperStopTimeout <= stop {
		t.Errorf("helperStopTimeout %v must exceed the helper's STOP_TIMEOUT %v so a stop is not killed mid-logout",
			helperStopTimeout, stop)
	}

	// The supervisor's wait for a previous loop must cover the whole worst-case
	// teardown (every stop attempt, then the WaitDelay backstop), or a genuinely
	// clean-but-slow logout is abandoned and reported as wedged.
	worst := time.Duration(helperStopAttempts)*helperStopTimeout + helperWaitDelay
	if s := New(nil, nil, nil); s.prevWait < worst {
		t.Errorf("prevWait %v must cover the worst-case teardown %v (%d*%v + %v)",
			s.prevWait, worst, helperStopAttempts, helperStopTimeout, helperWaitDelay)
	}

	// The logout window itself must be long enough to matter — a 1s window would
	// SIGKILL openconnect before its HTTPS logout completes.
	if stop < 5*time.Second {
		t.Errorf("STOP_TIMEOUT %v is too short for openconnect to complete its gateway logout", stop)
	}
}

// The helper must SIGINT openconnect (which triggers the clean logout) and only
// SIGKILL as a last resort AFTER the grace period — a KILL first would drop the
// tunnel without a logout, exactly the lingering-session bug.
func TestHelperStopSendsSIGINTBeforeKill(t *testing.T) {
	src := readHelperSource(t)
	int_ := strings.Index(src, "kill -INT")
	kill := strings.Index(src, "kill -KILL")
	if int_ < 0 {
		t.Fatal("helper must SIGINT openconnect so it logs out of the gateway on teardown")
	}
	if kill >= 0 && kill < int_ {
		t.Error("helper sends SIGKILL before SIGINT: openconnect would never send its logout")
	}
}

// The gateway comes from config.json, but the sudoers rule does not constrain
// the argument at all, so anything on this machine can call the helper with an
// argument of its choosing. Every one of these must be refused before openconnect
// is ever reached.
func TestHelperRejectsMalformedGateway(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell helper: macOS/Linux only")
	}
	// A pre-existing pidfile would make the no-side-effects assertion below
	// meaningless (it belongs to a real tunnel, not to us).
	if _, err := os.Stat(helperPIDFile); err == nil {
		t.Skipf("%s exists: a tunnel is running, skipping side-effect check", helperPIDFile)
	}

	tests := []struct {
		name    string
		gateway string
		wantErr string
	}{{
		name:    "openconnect option smuggled in: would run as root",
		gateway: "--script=/tmp/x",
		wantErr: "must not start with '-'",
	}, {
		name:    "bare flag",
		gateway: "-x",
		wantErr: "must not start with '-'",
	}, {
		name:    "too many colons",
		gateway: "a:b:c",
		wantErr: "must be host:port",
	}, {
		name:    "non-numeric port",
		gateway: "gw:abc",
		wantErr: "port must be numeric",
	}, {
		name:    "no port",
		gateway: "gw",
		wantErr: "must be host:port",
	}, {
		name:    "empty",
		gateway: "",
		wantErr: "must be host:port",
	}, {
		name:    "shell metacharacter",
		gateway: "gw:10443;id",
		wantErr: "invalid characters",
	}, {
		name:    "userinfo in the authority",
		gateway: "user@gw:443",
		wantErr: "invalid characters",
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runHelper(t, "start", tc.gateway)
			if code != 1 {
				t.Errorf("exit code = %d, want 1 (output: %q)", code, out)
			}
			if !strings.Contains(out, tc.wantErr) {
				t.Errorf("output = %q, want it to mention %q", out, tc.wantErr)
			}
			if _, err := os.Stat(helperPIDFile); err == nil {
				t.Errorf("helper wrote %s for a rejected gateway: validation must happen first", helperPIDFile)
			}
		})
	}
}

// A well-formed gateway must get past validation. The repo copy of the helper
// still carries the unsubstituted openconnect placeholder, so it stops at the
// install guard instead — asserting on that specific message is what
// distinguishes "validation accepted it" from "validation rejected it".
func TestHelperAcceptsWellFormedGatewayThenRequiresInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell helper: macOS/Linux only")
	}
	out, code := runHelper(t, "start", "host:10443")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (output: %q)", code, out)
	}
	if !strings.Contains(out, "not installed") {
		t.Errorf("output = %q, want the uninstalled-placeholder error: a well-formed "+
			"gateway must pass validation and fail on the missing openconnect path", out)
	}
	for _, rejected := range []string{"host:port", "invalid characters", "must not start with"} {
		if strings.Contains(out, rejected) {
			t.Errorf("well-formed gateway was rejected by validation: %q", out)
		}
	}
}

// The repo copy must never carry a real path: a committed absolute path would
// silently override what the installer resolved on the target machine.
func TestHelperRepoCopyKeepsPlaceholder(t *testing.T) {
	b, err := os.ReadFile(helperScript(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "OPENCONNECT='@OPENCONNECT@'") {
		t.Error("the repo helper must keep the @OPENCONNECT@ placeholder; " +
			"scripts/install.sh substitutes it with the path it resolved and verified")
	}
}

func TestHelperUnknownSubcommandIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell helper: macOS/Linux only")
	}
	out, code := runHelper(t, "exec", "/bin/sh")
	if code != 1 || !strings.Contains(out, "unknown subcommand") {
		t.Errorf("helper accepted an unknown subcommand: exit %d, output %q", code, out)
	}
}

// The app now threads the Advanced toggles through the helper as extra "start"
// arguments. Each must be validated against an exact allowlist. These are the
// only three flags that may pass; the repo copy still carries the openconnect
// placeholder, so a validated flag set falls through to the install guard, and
// asserting on "not installed" is what proves validation ACCEPTED it (that guard
// lives after validate_gateway/validate_flags and before any exec).
func TestHelperAcceptsAllowlistedFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell helper: macOS/Linux only")
	}
	tests := []struct {
		name string
		args []string
	}{
		{"no-dtls", []string{"--no-dtls"}},
		{"disable-ipv6", []string{"--disable-ipv6"}},
		{"servercert pin form", []string{"--servercert", "pin-sha256:AAAA"}},
		{"servercert sha256 byte form", []string{"--servercert", "sha256:AB:CD"}},
		{"servercert bare hex", []string{"--servercert", "FF"}},
		{"all three together", []string{"--no-dtls", "--disable-ipv6", "--servercert", "pin-sha256:AAAA"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runHelper(t, append([]string{"start", "host:10443"}, tc.args...)...)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1 (output: %q)", code, out)
			}
			if !strings.Contains(out, "not installed") {
				t.Errorf("output = %q, want it to reach the install guard: an allowlisted "+
					"flag set must pass validation, then fail on the missing openconnect path", out)
			}
			for _, rejected := range []string{"disallowed", "invalid characters", "requires a fingerprint", "look like a path"} {
				if strings.Contains(out, rejected) {
					t.Errorf("an allowlisted flag set was rejected by validation: %q", out)
				}
			}
		})
	}
}

// The security boundary: any argument that is not on the allowlist must be
// refused with a non-zero exit BEFORE openconnect (and before any pidfile side
// effect) is reached. If any of these slipped through, the helper would be back
// to letting a local caller pass arbitrary openconnect options as root.
func TestHelperRejectsDisallowedFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell helper: macOS/Linux only")
	}
	if _, err := os.Stat(helperPIDFile); err == nil {
		t.Skipf("%s exists: a tunnel is running, skipping side-effect check", helperPIDFile)
	}
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"script option would run code as root", []string{"--script=/tmp/x"}, "disallowed"},
		{"csd-wrapper option would run code as root", []string{"--csd-wrapper=/tmp/y"}, "disallowed"},
		{"generic openconnect flag", []string{"-o"}, "disallowed"},
		{"unknown long flag", []string{"--foo"}, "disallowed"},
		{"bare short flag", []string{"-x"}, "disallowed"},
		{"servercert without a fingerprint", []string{"--servercert"}, "requires a fingerprint"},
		{"servercert fingerprint that is a path", []string{"--servercert", "/tmp/evil"}, "look like a path"},
		{"servercert with metacharacters glued on", []string{"--servercert=x;rm"}, "disallowed"},
		{"servercert fingerprint with a shell metacharacter", []string{"--servercert", "AB;rm"}, "invalid characters"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runHelper(t, append([]string{"start", "host:10443"}, tc.args...)...)
			if code == 0 {
				t.Fatalf("helper accepted a disallowed argument (exit 0); output: %q", out)
			}
			if !strings.Contains(out, tc.wantErr) {
				t.Errorf("output = %q, want it to mention %q", out, tc.wantErr)
			}
			// Rejected before the openconnect path is even resolved: a disallowed
			// flag must not reach the install guard, let alone exec.
			if strings.Contains(out, "not installed") {
				t.Errorf("disallowed argument passed validation and reached openconnect resolution: %q", out)
			}
			if _, err := os.Stat(helperPIDFile); err == nil {
				t.Errorf("helper wrote %s for a rejected argument: validation must happen first", helperPIDFile)
			}
		})
	}
}

// stubbedHelper writes a copy of the privileged helper into dir whose openconnect
// is the given shell script and whose runtime paths (PIDFILE, CONFDIR) point
// inside dir, so an unprivileged test can run the real start path without write
// access to /var/run. It returns the helper's path.
func stubbedHelper(t *testing.T, dir, stubScript string) string {
	t.Helper()
	stub := filepath.Join(dir, "openconnect")
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(helperScript(t))
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(src), "OPENCONNECT='@OPENCONNECT@'", "OPENCONNECT='"+stub+"'", 1)
	patched = strings.Replace(patched, "PIDFILE=/var/run/openfortitray-openconnect.pid", "PIDFILE="+filepath.Join(dir, "pid"), 1)
	patched = strings.Replace(patched, "CONFDIR=/var/run/openfortitray-conf", "CONFDIR="+filepath.Join(dir, "conf"), 1)
	if !strings.Contains(patched, "OPENCONNECT='"+stub+"'") ||
		!strings.Contains(patched, "PIDFILE="+filepath.Join(dir, "pid")) ||
		!strings.Contains(patched, "CONFDIR="+filepath.Join(dir, "conf")) {
		t.Fatal("failed to patch the helper copy (placeholder, PIDFILE or CONFDIR line changed?)")
	}
	helper := filepath.Join(dir, "openfortitray-tunnel")
	if err := os.WriteFile(helper, []byte(patched), 0o755); err != nil {
		t.Fatal(err)
	}
	return helper
}

// End to end: a validated flag set actually reaches openconnect's argv, in the
// order internal/tunnel builds (fixed flags, then the toggles, then the
// gateway). Darwin only: the run has to get past resolve_openconnect, and on
// Linux that step refuses a stub openconnect that is not root-owned (by design —
// see the threat model). On macOS the user-owned Homebrew prefix is tolerated
// with a warning, so the stub runs. The helper's PIDFILE is also redirected into
// the temp dir so the exec path does not need write access to /var/run.
func TestHelperAllowlistedFlagsReachOpenconnectArgv(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("resolve_openconnect refuses a non-root-owned stub on Linux; exec path is Darwin-testable only")
	}
	dir := t.TempDir()
	ocArgv := filepath.Join(dir, "oc.argv")
	// Stub openconnect: swallow the cookie on stdin, record its argv, succeed.
	stub := filepath.Join(dir, "openconnect")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' \"$*\" > "+ocArgv+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	src, err := os.ReadFile(helperScript(t))
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(src), "OPENCONNECT='@OPENCONNECT@'", "OPENCONNECT='"+stub+"'", 1)
	patched = strings.Replace(patched, "PIDFILE=/var/run/openfortitray-openconnect.pid", "PIDFILE="+filepath.Join(dir, "pid"), 1)
	// CONFDIR holds the short-lived openconnect config file carrying the cookie.
	// It lives under /var/run in production (root-only); redirect it too, or an
	// unprivileged test run cannot create it.
	patched = strings.Replace(patched, "CONFDIR=/var/run/openfortitray-conf", "CONFDIR="+filepath.Join(dir, "conf"), 1)
	if !strings.Contains(patched, "OPENCONNECT='"+stub+"'") ||
		!strings.Contains(patched, "PIDFILE="+filepath.Join(dir, "pid")) ||
		!strings.Contains(patched, "CONFDIR="+filepath.Join(dir, "conf")) {
		t.Fatal("failed to patch the helper copy (placeholder, PIDFILE or CONFDIR line changed?)")
	}
	helper := filepath.Join(dir, "openfortitray-tunnel")
	if err := os.WriteFile(helper, []byte(patched), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", helper, "start", "gw:10443",
		"--no-dtls", "--disable-ipv6", "--servercert", "pin-sha256:AAAA")
	cmd.Stdin = strings.NewReader("COOKIE\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stubbed helper failed to exec openconnect: %v (output: %q)", err, out)
	}

	got, err := os.ReadFile(ocArgv)
	if err != nil {
		t.Fatalf("openconnect was never exec'd with the validated flags: %v", err)
	}
	argv := strings.TrimSpace(string(got))
	conf := filepath.Join(dir, "conf", "openconnect.conf")
	want := "--protocol=fortinet --config " + conf +
		" --non-inter --no-dtls --disable-ipv6 --servercert pin-sha256:AAAA gw:10443"
	if argv != want {
		t.Errorf("openconnect argv = %q, want %q", argv, want)
	}
	// The cookie must never reach the argv: it would then be visible in `ps` for
	// the tunnel's whole life.
	if strings.Contains(argv, "COOKIE") {
		t.Errorf("the session cookie leaked into openconnect's argv: %q", argv)
	}
}

// The helper hands the cookie to openconnect through a config file because
// --cookie-on-stdin truncates at 1024 bytes and silently breaks longer cookies
// (1288-byte cookies observed from this gateway, rejected with an opaque "Cookie
// was rejected by server"). So the file must carry the cookie WHOLE.
func TestHelperConfigFileCarriesTheFullCookie(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX helper only")
	}
	dir := t.TempDir()
	// Long enough to have been truncated by the old path, and made of the
	// characters a real SVPNCOOKIE uses.
	long := strings.Repeat("aB3._~%=+/-", 130) // 1430 chars
	confDump := filepath.Join(dir, "conf-dump")
	helper := stubbedHelper(t, dir, `#!/bin/sh
# Record the config file openconnect was handed, before the helper unlinks it.
for a in "$@"; do
  case "$prev" in --config) cp "$a" `+confDump+` ;; esac
  prev=$a
done
exit 0
`)
	cmd := exec.Command("/bin/sh", helper, "start", "gw:10443")
	cmd.Stdin = strings.NewReader(long + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper failed: %v (%s)", err, out)
	}

	data, err := os.ReadFile(confDump)
	if err != nil {
		t.Fatalf("openconnect was not given a config file: %v", err)
	}
	want := "cookie=SVPNCOOKIE=" + long
	if strings.TrimSpace(string(data)) != want {
		t.Errorf("config file = %q,\nwant the cookie whole (%d chars)", string(data), len(long))
	}
}

// SECURITY: the config file format accepts any openconnect option, including
// script=, which openconnect runs as ROOT. So a cookie must never be able to
// introduce a second config line.
//
// Two defences, both asserted here. The helper reads the cookie with a single
// `read -r`, so anything after a newline is never read at all; and validate_cookie
// restricts the value to the characters a real SVPNCOOKIE uses, so a value with
// whitespace (which could otherwise carry an option on the same line) is refused
// outright.
func TestHelperCookieCannotInjectConfigOptions(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("resolve_openconnect refuses a non-root-owned stub on Linux")
	}
	dir := t.TempDir()
	confDump := filepath.Join(dir, "conf-dump")
	helper := stubbedHelper(t, dir, "#!/bin/sh\nprev=\nfor a in \"$@\"; do\n  if [ \"$prev\" = --config ]; then cp \"$a\" "+confDump+"; fi\n  prev=$a\ndone\nexit 0\n")

	cmd := exec.Command("/bin/sh", helper, "start", "gw:10443")
	cmd.Stdin = strings.NewReader("GOODCOOKIE\nscript=/tmp/evil\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper failed: %v (%s)", err, out)
	}
	data, err := os.ReadFile(confDump)
	if err != nil {
		t.Fatalf("no config file was produced: %v", err)
	}
	if strings.Contains(string(data), "script=") {
		t.Errorf("a newline in the cookie injected a config option — openconnect runs script= as root:\n%s", data)
	}
	if got := strings.TrimSpace(string(data)); got != "cookie=SVPNCOOKIE=GOODCOOKIE" {
		t.Errorf("config file = %q, want only the first line's cookie", got)
	}
}

// Values that could carry an option on the SAME line, or be mistaken for a flag,
// must be refused before openconnect is started at all.
func TestHelperRejectsUnsafeCookies(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("resolve_openconnect refuses a non-root-owned stub on Linux")
	}
	for _, tc := range []struct{ name, cookie string }{
		{"space could carry another option", "GOOD script=/tmp/evil"},
		{"tab", "GOOD\tscript=/tmp/evil"},
		{"leading dash looks like a flag", "-oProxyCommand=evil"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "ran")
			helper := stubbedHelper(t, dir, "#!/bin/sh\ntouch "+marker+"\nexit 0\n")
			cmd := exec.Command("/bin/sh", helper, "start", "gw:10443")
			cmd.Stdin = strings.NewReader(tc.cookie + "\n")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Errorf("helper accepted a cookie it must reject: %q (output %s)", tc.cookie, out)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Error("openconnect was started with an invalid cookie")
			}
		})
	}
}

// dnsHelper writes a copy of the privileged helper whose RESOLVER_DIR is
// redirected into resolverDir, so the dns-set/dns-clear tests exercise the real
// script (its validation is the security boundary) without touching the real
// /etc/resolver. It never runs as root — a temp dir is user-writable.
func dnsHelper(t *testing.T, resolverDir string) string {
	t.Helper()
	src, err := os.ReadFile(helperScript(t))
	if err != nil {
		t.Fatal(err)
	}
	// Single-quote the replacement: a t.TempDir() path can contain characters the
	// subtest name carried (e.g. parentheses), which would be a shell syntax error
	// in a bare assignment.
	patched := strings.Replace(string(src), "RESOLVER_DIR=/etc/resolver", "RESOLVER_DIR='"+resolverDir+"'", 1)
	if !strings.Contains(patched, "RESOLVER_DIR='"+resolverDir+"'") {
		t.Fatal("failed to patch RESOLVER_DIR (line changed?)")
	}
	helper := filepath.Join(t.TempDir(), "openfortitray-tunnel")
	if err := os.WriteFile(helper, []byte(patched), 0o755); err != nil {
		t.Fatal(err)
	}
	return helper
}

func runDNSHelper(t *testing.T, helper string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{helper}, args...)...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running helper: %v", err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

// dns-set writes a scoped resolver per domain, all pointing at the one DNS IP,
// each carrying our marker so dns-clear can recognise it, mode 0644.
func TestHelperDNSSetWritesResolverFileWithMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell helper: macOS/Linux only")
	}
	resolverDir := t.TempDir()
	helper := dnsHelper(t, resolverDir)

	out, code := runDNSHelper(t, helper, "dns-set", "192.0.2.53", "corp.example", "svc.corp.internal")
	if code != 0 {
		t.Fatalf("dns-set exited %d (output: %q)", code, out)
	}
	for _, domain := range []string{"corp.example", "svc.corp.internal"} {
		f := filepath.Join(resolverDir, domain)
		info, err := os.Stat(f)
		if err != nil {
			t.Fatalf("dns-set did not create %s: %v", f, err)
		}
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Errorf("%s mode = %o, want 0644", f, perm)
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		if !strings.Contains(got, "# openfortitray-managed") {
			t.Errorf("%s missing the managed marker; got %q", f, got)
		}
		if !strings.Contains(got, "nameserver 192.0.2.53") {
			t.Errorf("%s missing the nameserver line; got %q", f, got)
		}
	}
}

// The security boundary for the DNS subcommands: a malformed IP or a domain
// carrying a path/metacharacter/leading-dash must be refused with a non-zero
// exit and, crucially, NO file written — the domain becomes a filename under
// /etc/resolver, so a '/' or '..' would otherwise aim a root write off target.
func TestHelperDNSSetRejectsInjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell helper: macOS/Linux only")
	}
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"octet out of range", []string{"300.1.2.3", "corp.private"}, "not a valid IPv4/IPv6"},
		{"ip is a flag", []string{"-nameserver", "corp.private"}, "must not start with '-'"},
		{"ip has metacharacter", []string{"10.0.0.1;id", "corp.private"}, "invalid characters"},
		{"ip is a path", []string{"/etc/passwd", "corp.private"}, "invalid characters"},
		{"domain with slash (path traversal)", []string{"192.0.2.53", "../../etc/cron.d/x"}, "invalid characters"},
		{"domain with a bare slash", []string{"192.0.2.53", "a/b"}, "invalid characters"},
		{"domain with a shell metacharacter", []string{"192.0.2.53", "a;rm -rf"}, "invalid characters"},
		{"domain with a space", []string{"192.0.2.53", "a b"}, "invalid characters"},
		{"domain starting with a dash", []string{"192.0.2.53", "-x"}, "must not start with '-'"},
		{"domain is dotdot", []string{"192.0.2.53", ".."}, "must not be '.' or '..'"},
		{"empty domain", []string{"192.0.2.53", ""}, "must not be empty"},
		{"no domain at all", []string{"192.0.2.53"}, "usage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolverDir := t.TempDir()
			helper := dnsHelper(t, resolverDir)
			out, code := runDNSHelper(t, helper, append([]string{"dns-set"}, tc.args...)...)
			if code == 0 {
				t.Fatalf("dns-set accepted an injection attempt (exit 0); output: %q", out)
			}
			if !strings.Contains(out, tc.wantErr) {
				t.Errorf("output = %q, want it to mention %q", out, tc.wantErr)
			}
			entries, err := os.ReadDir(resolverDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("a rejected dns-set still wrote %d file(s) into the resolver dir; validation must happen first", len(entries))
			}
		})
	}
}

// dns-clear removes only files WE stamped, never a pre-existing resolver file a
// VPN client or the admin left behind.
func TestHelperDNSClearRemovesOnlyMarkedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell helper: macOS/Linux only")
	}
	resolverDir := t.TempDir()
	helper := dnsHelper(t, resolverDir)

	// One of ours (created via dns-set), and one foreign file with no marker.
	if _, code := runDNSHelper(t, helper, "dns-set", "192.0.2.53", "corp.example"); code != 0 {
		t.Fatal("setup dns-set failed")
	}
	foreign := filepath.Join(resolverDir, "database.windows.net")
	if err := os.WriteFile(foreign, []byte("nameserver 9.9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Ask to clear both domains: ours must go, the foreign one must remain.
	out, code := runDNSHelper(t, helper, "dns-clear", "corp.example", "database.windows.net")
	if code != 0 {
		t.Fatalf("dns-clear exited %d (output: %q)", code, out)
	}
	if _, err := os.Stat(filepath.Join(resolverDir, "corp.example")); !os.IsNotExist(err) {
		t.Errorf("dns-clear did not remove our own resolver file (err=%v)", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("dns-clear removed a foreign, unmarked resolver file: %v", err)
	}
}

// dns-clear validates its domains too, so a '/'-bearing argument can never reach
// the rm, and it is idempotent (nothing of ours to remove is success).
func TestHelperDNSClearValidatesAndIsIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell helper: macOS/Linux only")
	}
	resolverDir := t.TempDir()
	helper := dnsHelper(t, resolverDir)

	out, code := runDNSHelper(t, helper, "dns-clear", "a/b")
	if code == 0 || !strings.Contains(out, "invalid characters") {
		t.Errorf("dns-clear accepted a slash-bearing domain: exit %d, output %q", code, out)
	}
	// Nothing of ours present: still succeeds.
	if out, code := runDNSHelper(t, helper, "dns-clear", "never-set.corp"); code != 0 {
		t.Errorf("dns-clear on a missing domain must be idempotent: exit %d, output %q", code, out)
	}
}
