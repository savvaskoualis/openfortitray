package tunnel

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
