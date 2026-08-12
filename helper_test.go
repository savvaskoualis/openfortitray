package openfortitray

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The embedded helper must be the real scripts/openfortitray-tunnel, complete
// with the unsubstituted placeholder the install rewrites. If this drifts, the
// bootstrap would install a stale or already-substituted helper.
func TestEmbeddedHelperMatchesRepoAndCarriesPlaceholder(t *testing.T) {
	if !strings.Contains(helperScript, placeholderLine) {
		t.Fatalf("embedded helper does not contain %q", placeholderLine)
	}
	if strings.Count(helperScript, placeholderLine) != 1 {
		t.Fatalf("embedded helper must contain exactly one %q line", placeholderLine)
	}
	if !strings.HasPrefix(helperScript, "#!/bin/sh") {
		t.Errorf("embedded helper does not start with the expected shebang")
	}
	on, err := os.ReadFile(filepath.Join("scripts", "openfortitray-tunnel"))
	if err != nil {
		t.Fatal(err)
	}
	if string(on) != helperScript {
		t.Error("embedded helper text differs from scripts/openfortitray-tunnel on disk")
	}
}

func TestBuildRootInstallScriptContent(t *testing.T) {
	script, err := buildRootInstallScript("alice", "/opt/homebrew/bin/openconnect", "/var/folders/xy/T/openfortitray-bootstrap-123")
	if err != nil {
		t.Fatal(err)
	}
	wantContains := []string{
		// The three single-quoted, validated tokens plus the embedded sha.
		"PRINCIPAL='alice'\n",
		"OC='/opt/homebrew/bin/openconnect'\n",
		"WORKDIR='/var/folders/xy/T/openfortitray-bootstrap-123'\n",
		"EXPECTED_SHA='" + helperSHA256() + "'\n",
		// The substitution match line and the count guard.
		"if [ \"$line\" = \"OPENCONNECT='@OPENCONNECT@'\" ]; then",
		"printf \"OPENCONNECT='%s'\\n\" \"$OC\"",
		// visudo validation before the sudoers file goes live.
		"visudo -c -f \"$RTMP/sudoers\"",
		// The scoped NOPASSWD rule, helper path only.
		`RULE="$PRINCIPAL ALL=(root) NOPASSWD: $HELPER_TARGET"`,
		// sha re-verification of the temp helper as root.
		`[ "$actual" = "$EXPECTED_SHA" ]`,
		// end-to-end verify through the principal.
		`sudo -u "$PRINCIPAL" sudo -n "$HELPER_TARGET" stop`,
		// install modes.
		`install -o root -g wheel -m 0755 "$RTMP/built"`,
		`install -o root -g wheel -m 0440 "$RTMP/sudoers"`,
	}
	for _, w := range wantContains {
		if !strings.Contains(script, w) {
			t.Errorf("generated script missing %q", w)
		}
	}
	// No user-typed value may appear: the embedded helper text must NOT be spliced
	// into the privileged string (it travels via the temp file). A cheap proxy: the
	// helper's own DNS marker comment must not be present in the root script.
	if strings.Contains(script, "openfortitray-managed") {
		t.Error("the embedded helper text leaked into the privileged shell script; it must travel via the temp file only")
	}
}

func TestBuildRootInstallScriptRejectsBadInputs(t *testing.T) {
	goodOC := "/opt/homebrew/bin/openconnect"
	goodWD := "/tmp/openfortitray-bootstrap-1"
	tests := []struct {
		name          string
		principal, oc string
		workdir       string
	}{
		{"principal empty", "", goodOC, goodWD},
		{"principal root", "root", goodOC, goodWD},
		{"principal with quote", "al'ice", goodOC, goodWD},
		{"principal with space", "al ice", goodOC, goodWD},
		{"principal with semicolon", "alice;id", goodOC, goodWD},
		{"principal with dollar", "al$ice", goodOC, goodWD},
		{"principal with backslash", `al\ice`, goodOC, goodWD},
		{"oc relative", "alice", "openconnect", goodWD},
		{"oc empty", "alice", "", goodWD},
		{"oc with space", "alice", "/opt/home brew/openconnect", goodWD},
		{"oc with quote", "alice", "/opt/'/openconnect", goodWD},
		{"oc with semicolon", "alice", "/opt/oc;rm", goodWD},
		{"oc with backtick", "alice", "/opt/`id`/oc", goodWD},
		{"workdir relative", "alice", goodOC, "tmp/x"},
		{"workdir with quote", "alice", goodOC, "/tmp/x'y"},
		{"workdir with space", "alice", goodOC, "/tmp/x y"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildRootInstallScript(tc.principal, tc.oc, tc.workdir); err == nil {
				t.Errorf("buildRootInstallScript(%q,%q,%q) = nil error, want rejection",
					tc.principal, tc.oc, tc.workdir)
			}
		})
	}
}

// The generated privileged script must be valid POSIX shell (osascript runs it
// under /bin/sh). sh -n only parses, it does not execute, so this is safe.
func TestBuildRootInstallScriptIsValidShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	script, err := buildRootInstallScript("alice", "/opt/homebrew/bin/openconnect", "/tmp/openfortitray-bootstrap-1")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated script is not valid shell: %v\n%s", err, out)
	}
}

func TestEscapeAppleScript(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "echo hello", "echo hello"},
		{"double quote", `say "hi"`, `say \"hi\"`},
		{"backslash", `a\b`, `a\\b`},
		{"backslash then quote", "\\\"", `\\\"`},
		{"quote then backslash", "\"\\", `\"\\`},
		{"newline", "a\nb", `a\nb`},
		{"carriage return", "a\rb", `a\rb`},
		{"tab", "a\tb", `a\tb`},
		{"mixed", "x=\"$y\"\n\\z", `x=\"$y\"\n\\z`},
		{"dollar and backtick untouched", "$x `id`", "$x `id`"},
		{"single quote untouched", `it's`, `it's`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeAppleScript(tc.in); got != tc.want {
				t.Errorf("escapeAppleScript(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// End-to-end proof of the escaping: a shell script full of quotes, backslashes
// and a tab, once escaped into an AppleScript literal and run by osascript
// (as the current user — NO admin privileges), must produce byte-for-byte the
// same output as running it directly under /bin/sh.
func TestEscapeAppleScriptRoundTripViaOsascript(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("osascript is macOS only")
	}
	if _, err := exec.LookPath("osascript"); err != nil {
		t.Skip("osascript not available")
	}
	shell := `printf 'quote=" back=\\ tab=\t dollar=$x done'`

	direct, err := exec.Command("/bin/sh", "-c", shell).CombinedOutput()
	if err != nil {
		t.Fatalf("running the probe script directly failed: %v", err)
	}
	apple := `do shell script "` + escapeAppleScript(shell) + `"`
	viaOsa, err := exec.Command("osascript", "-e", apple).CombinedOutput()
	if err != nil {
		t.Fatalf("running the escaped script via osascript failed: %v\n%s", err, viaOsa)
	}
	// do shell script strips one trailing newline; osascript then appends one.
	got := strings.TrimRight(string(viaOsa), "\n")
	want := strings.TrimRight(string(direct), "\n")
	if got != want {
		t.Errorf("osascript round-trip mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestHelperReadyAt(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "openfortitray-tunnel")
	if err := os.WriteFile(present, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "nope")

	tests := []struct {
		name string
		path string
		stop func() error
		want bool
	}{
		{"missing file is not ready", missing, func() error { return nil }, false},
		{"present + passwordless stop ok", present, func() error { return nil }, true},
		{"present + stop prompts/fails", present, func() error { return exec.ErrNotFound }, false},
		{"dir is not a helper", dir, func() error { return nil }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := helperReadyAt(tc.path, tc.stop); got != tc.want {
				t.Errorf("helperReadyAt = %v, want %v", got, tc.want)
			}
		})
	}
}

// installWith must be idempotent: when the helper is already ready it runs
// nothing and returns nil; otherwise it runs the install exactly once.
func TestInstallWithIdempotency(t *testing.T) {
	t.Run("ready: does not run", func(t *testing.T) {
		ran := false
		err := installWith(func() bool { return true }, func() error { ran = true; return nil })
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if ran {
			t.Error("install ran even though the helper was already ready")
		}
	})
	t.Run("not ready: runs once", func(t *testing.T) {
		n := 0
		err := installWith(func() bool { return false }, func() error { n++; return nil })
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if n != 1 {
			t.Errorf("install ran %d times, want 1", n)
		}
	})
}

func TestIsUserCancel(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"execution error: User canceled. (-128)", true},
		{"User cancelled", true},
		{"some other failure", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := isUserCancel(tc.in); got != tc.want {
			t.Errorf("isUserCancel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestValidators(t *testing.T) {
	if err := validatePrincipal("savvask"); err != nil {
		t.Errorf("valid principal rejected: %v", err)
	}
	if err := validateOpenconnectPath("/opt/homebrew/bin/openconnect"); err != nil {
		t.Errorf("valid openconnect rejected: %v", err)
	}
	if err := validateOpenconnectPath("relative/path"); err == nil {
		t.Error("relative openconnect path accepted")
	}
	if err := validateWorkdir("/var/folders/ab/T/x-1"); err != nil {
		t.Errorf("valid workdir rejected: %v", err)
	}
}
