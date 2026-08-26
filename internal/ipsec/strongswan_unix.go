//go:build darwin || linux

package ipsec

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

// swanctlInstallHint leads the detail of a "swanctl not found" permanent
// failure — the one strongSwan-side case retrying can never fix, since the
// binary genuinely is not on PATH.
const swanctlInstallHint = "strongSwan's swanctl was not found on PATH — install strongSwan (e.g. `brew install strongswan` on macOS, or your distro's strongswan/strongswan-swanctl package on Linux)."

// classifySwanctlLoadAllErr wraps a `swanctl --load-all` failure as
// tunnel.ErrPermanent when swanctl itself was not found on PATH — the one
// case retrying can never fix, since the binary genuinely is not there.
// Every other --load-all failure (charon not up yet, a config syntax error,
// a transient permission issue, ...) is left unwrapped, so the Supervisor's
// normal backoff/retry still applies exactly as before. Split out from
// NewStrongSwanRunFunc so it is directly unit-testable with a synthetic
// error, without shelling out to a real (or deliberately absent) swanctl.
func classifySwanctlLoadAllErr(err error, out []byte) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%w: %s\nipsec: swanctl --load-all: %v: %s",
			tunnel.ErrPermanent, swanctlInstallHint, err, out)
	}
	return fmt.Errorf("ipsec: swanctl --load-all: %w: %s", err, out)
}

// connName is the swanctl connection name this app always uses, so a
// previous run's fragment is cleanly replaced rather than accumulating
// stale entries under a name derived from user input.
const connName = "oft-tun"

// swanctlConfDirCandidates lists the swanctl conf.d directories to probe,
// in order, for where to write this connection's config fragments. A
// standard Linux distro package (built with --sysconfdir=/etc) uses
// /etc/swanctl/conf.d, per swanctl.conf(5)'s "conn dir" default. Homebrew
// on macOS does NOT: verified by installing strongSwan 6.0.7 via
// `brew install strongswan` on this machine and inspecting the layout —
// its swanctl.conf lives under ${HOMEBREW_PREFIX}/etc/swanctl, i.e.
// /opt/homebrew/etc/swanctl/conf.d on Apple Silicon or
// /usr/local/etc/swanctl/conf.d on Intel, confirmed both by `ls` on the
// installed tree and by `strings` on the swanctl binary (which embeds
// "/opt/homebrew/etc/swanctl" as its compiled-in sysconfdir). Hardcoding
// /etc/swanctl/conf.d alone would silently write fragments swanctl never
// includes on a Homebrew install.
var swanctlConfDirCandidates = []string{
	"/etc/swanctl/conf.d",
	"/opt/homebrew/etc/swanctl/conf.d",
	"/usr/local/etc/swanctl/conf.d",
}

// swanctlDir returns the swanctl conf.d directory to write this
// connection's fragments into: the first candidate whose swanctl.conf
// directory already exists (i.e. an install actually put it there),
// falling back to the standard Linux distro location, which MkdirAll
// will then create.
func swanctlDir() string {
	for _, dir := range swanctlConfDirCandidates {
		if _, err := os.Stat(filepath.Dir(dir)); err == nil {
			return dir
		}
	}
	return swanctlConfDirCandidates[0]
}

// establishedRe matches charon's CHILD_SA-established log line and
// captures the assigned (client-side) traffic selector's address. Verified
// against strongSwan 6.0.7's compiled libcharon: `strings
// libcharon.0.dylib` confirms the exact format string "CHILD_SA %s{%u}
// established with SPIs %.8x_i %.8x_o and TS %#R === %#R" (child_sa.c).
// No line-start anchor, since charon sometimes prefixes this with a
// qualifier (e.g. "outbound CHILD_SA ... established ..."), and the
// substring match still finds it either way.
var establishedRe = regexp.MustCompile(
	`CHILD_SA ` + connName + `\{\d+\} established .* TS \S+ === (\d+\.\d+\.\d+\.\d+)`)

// failedRe matches swanctl's own CLI summary line printed when
// `--initiate` fails, e.g. "initiate failed: establishing CHILD_SA
// failed". Verified against the compiled swanctl 6.0.7 binary: `strings
// swanctl` confirms the literal format string "initiate failed: %s"
// (swanctl.c) alongside "initiate completed successfully" for the success
// case. charon's own log has no single fixed "failed" line — the possible
// underlying reasons vary (peer not responding, no proposal chosen, auth
// failed, ...) — so matching swanctl's own terminal status line, which is
// unconditionally printed to the same stdout/stderr stream this code
// scans, is what's actually reliable to match here.
var failedRe = regexp.MustCompile(`^initiate failed:`)

// parseCharonLine reports whether line is charon's established-with-IP
// line, swanctl's initiate-failed summary line, or neither.
func parseCharonLine(line string) (ip string, established, failed bool) {
	if m := establishedRe.FindStringSubmatch(line); m != nil {
		return m[1], true, false
	}
	if failedRe.MatchString(line) {
		return "", false, true
	}
	return "", false, false
}

// swanctlConnFragment renders this profile's swanctl.conf connection
// block. PSK/cert secrets are written separately by writeSecretsFragment,
// never interpolated into this fragment.
func swanctlConnFragment(p config.Profile) string {
	authLine := "auth = psk"
	certsLine := ""
	if p.IPsec.AuthMethod == config.IPsecAuthCert {
		authLine = "auth = pubkey"
		certsLine = fmt.Sprintf("\n            certs = %s", p.IPsec.CertPath)
	}
	return fmt.Sprintf(`connections {
    %s {
        version = 2
        remote_addrs = %s
        local {
            %s%s
            id = %s
        }
        remote {
            id = %s
        }
        children {
            %s {
                local_ts = 0.0.0.0/0
                remote_ts = 0.0.0.0/0
                esp_proposals = %s
            }
        }
        proposals = %s
    }
}
`, connName, p.Gateway, authLine, certsLine, ipsecLocalID(p), p.IPsec.RemoteID,
		connName, p.IPsec.ESPProposal, p.IPsec.IKEProposal)
}

// ipsecLocalID returns the profile's configured local ID, or a sensible
// default (%any lets charon pick based on the auth method) when unset.
func ipsecLocalID(p config.Profile) string {
	if p.IPsec.LocalID != "" {
		return p.IPsec.LocalID
	}
	return "%any"
}

// writeSecretsFragment writes the PSK secret (or references the
// configured cert/key paths) to swanctl's secrets fragment, scoped to
// this connection and world-unreadable.
func writeSecretsFragment(p config.Profile, psk string) error {
	var body string
	switch p.IPsec.AuthMethod {
	case config.IPsecAuthCert:
		body = fmt.Sprintf("private {\n    file = %s\n}\n", p.IPsec.KeyPath)
	default:
		body = fmt.Sprintf("ike-%s {\n    id-1 = %s\n    id-2 = %s\n    secret = %q\n}\n",
			connName, ipsecLocalID(p), p.IPsec.RemoteID, psk)
	}
	path := filepath.Join(swanctlDir(), connName+".secrets.conf")
	return os.WriteFile(path, []byte(body), 0o600)
}

// NewStrongSwanRunFunc returns the RunFunc that drives strongSwan's
// swanctl for profile, using psk (ignored unless AuthMethod == IPsecAuthPSK).
func NewStrongSwanRunFunc(p config.Profile, psk string) RunFunc {
	return func(ctx context.Context, connected func(ip string)) error {
		dir := swanctlDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("ipsec: creating %s: %w", dir, err)
		}
		connPath := filepath.Join(dir, connName+".conf")
		if err := os.WriteFile(connPath, []byte(swanctlConnFragment(p)), 0o644); err != nil {
			return fmt.Errorf("ipsec: writing swanctl config: %w", err)
		}
		defer os.Remove(connPath)

		secretsPath := filepath.Join(dir, connName+".secrets.conf")
		if err := writeSecretsFragment(p, psk); err != nil {
			return fmt.Errorf("ipsec: writing swanctl secrets: %w", err)
		}
		defer os.Remove(secretsPath)

		if out, err := exec.CommandContext(ctx, "swanctl", "--load-all").CombinedOutput(); err != nil {
			return classifySwanctlLoadAllErr(err, out)
		}

		initCtx, cancelInit := context.WithCancel(ctx)
		defer cancelInit()
		cmd := exec.CommandContext(initCtx, "swanctl", "--initiate", "-c", connName, "--loglevel", "2")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("ipsec: swanctl --initiate stdout pipe: %w", err)
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("ipsec: starting swanctl --initiate: %w", err)
		}

		established := false
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			ip, ok, failed := parseCharonLine(line)
			if ok {
				established = true
				connected(ip)
				break // swanctl --initiate is one-shot; charon keeps the SA up as a daemon from here — health is polled below, not read from this exited process
			}
			if failed {
				break
			}
		}

		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			terminateCmd := exec.Command("swanctl", "--terminate", "-c", connName)
			_ = terminateCmd.Run()
			return ctx.Err()
		}
		if !established {
			if waitErr != nil {
				return fmt.Errorf("ipsec: swanctl --initiate: %w", waitErr)
			}
			return fmt.Errorf("ipsec: swanctl --initiate exited without establishing the tunnel")
		}

		// swanctl --initiate has already exited (it's a one-shot trigger, not a
		// long-running process) — charon keeps the SA up as its own background
		// daemon from here. Poll --list-sas for as long as the SA is still
		// installed; a drop (DPD timeout, gateway-initiated teardown) shows up
		// as the connection name disappearing from the list, which is the
		// signal to return an error so the Supervisor's backoff/reconnect loop
		// (Task 2) takes over — without this poll, a drop after a successful
		// connect would never be noticed at all.
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				terminateCmd := exec.Command("swanctl", "--terminate", "-c", connName)
				_ = terminateCmd.Run()
				return ctx.Err()
			case <-ticker.C:
				out, err := exec.CommandContext(ctx, "swanctl", "--list-sas", "-c", connName).CombinedOutput()
				if err != nil || !bytes.Contains(out, []byte(connName)) {
					return fmt.Errorf("ipsec: %s SA no longer installed", connName)
				}
			}
		}
	}
}
