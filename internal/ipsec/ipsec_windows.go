//go:build windows

package ipsec

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/savvaskoualis/openfortitray/internal/config"
)

// vpnConnectionName is the Windows VPN connection profile name this app
// always uses, so re-Connecting cleanly replaces any previous profile
// rather than accumulating stale ones under a name derived from user input.
const vpnConnectionName = "OpenFortiTray IPsec"

type vpnStatus int

const (
	vpnDisconnected vpnStatus = iota
	vpnConnecting
	vpnConnected
)

// parseVpnConnectionStatus maps Get-VpnConnection's ConnectionStatus text
// (verified against Microsoft's VpnConnection cmdlet docs: Connected,
// Connecting, Disconnected, Disconnecting) onto vpnStatus. Anything
// unrecognized — including "Disconnecting" — reports vpnDisconnected:
// failing closed means a wedged/renamed state is treated as "not up" and
// retried, never mistaken for a live tunnel.
func parseVpnConnectionStatus(s string) vpnStatus {
	switch strings.TrimSpace(s) {
	case "Connected":
		return vpnConnected
	case "Connecting":
		return vpnConnecting
	default:
		return vpnDisconnected
	}
}

// addVpnConnectionArgs builds Add-VpnConnection's argument list for p.
// PSK/cert secrets are passed via a separate secured-string argument in
// runPowerShell, never interpolated into this slice as plain text.
func addVpnConnectionArgs(p config.Profile) []string {
	authMethod := "PSK"
	if p.IPsec.AuthMethod == config.IPsecAuthCert {
		authMethod = "MachineCertificate"
	}
	return []string{
		"-Name", vpnConnectionName,
		"-ServerAddress", p.Gateway,
		"-TunnelType", "IKEv2",
		"-AuthenticationMethod", authMethod,
		"-EncryptionLevel", "Required",
		"-Force",
	}
}

// runPowerShell runs a PowerShell command with args, returning combined
// output. Every call here is best-effort: callers log and degrade rather
// than panic on a missing/broken PowerShell — see NewWindowsRunFunc.
func runPowerShell(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"-NoProfile", "-NonInteractive", "-Command"}, args...)
	out, err := exec.CommandContext(ctx, "powershell.exe", full...).CombinedOutput()
	return string(out), err
}

// NewWindowsRunFunc returns the RunFunc that drives Windows' native IKEv2
// VPN stack for profile, using psk (ignored unless AuthMethod ==
// IPsecAuthPSK).
func NewWindowsRunFunc(p config.Profile, psk string) RunFunc {
	return func(ctx context.Context, connected func(ip string)) error {
		addArgs := addVpnConnectionArgs(p)
		if p.IPsec.AuthMethod == config.IPsecAuthPSK {
			addArgs = append(addArgs, "-L2tpPsk", psk)
		}
		cmdline := fmt.Sprintf("Remove-VpnConnection -Name %q -Force -ErrorAction SilentlyContinue; Add-VpnConnection %s",
			vpnConnectionName, strings.Join(quoteArgs(addArgs), " "))
		if out, err := runPowerShell(ctx, cmdline); err != nil {
			return fmt.Errorf("ipsec: Add-VpnConnection: %w: %s", err, out)
		}

		if out, err := runPowerShell(ctx, fmt.Sprintf("rasdial %q", vpnConnectionName)); err != nil {
			return fmt.Errorf("ipsec: rasdial: %w: %s", err, out)
		}

		poll := time.NewTicker(2 * time.Second)
		defer poll.Stop()
		reportedConnected := false
		for {
			select {
			case <-ctx.Done():
				_, _ = runPowerShell(context.Background(),
					fmt.Sprintf("rasdial %q /disconnect", vpnConnectionName))
				return ctx.Err()
			case <-poll.C:
				out, err := runPowerShell(ctx, fmt.Sprintf(
					"(Get-VpnConnection -Name %q).ConnectionStatus", vpnConnectionName))
				if err != nil {
					return fmt.Errorf("ipsec: Get-VpnConnection: %w: %s", err, out)
				}
				switch parseVpnConnectionStatus(out) {
				case vpnConnected:
					if !reportedConnected {
						reportedConnected = true
						ip, _ := runPowerShell(ctx, fmt.Sprintf(
							"(Get-VpnConnection -Name %q).ClientIPAddress", vpnConnectionName))
						connected(strings.TrimSpace(ip))
					}
				case vpnDisconnected:
					if reportedConnected {
						return fmt.Errorf("ipsec: VPN connection dropped")
					}
				}
			}
		}
	}
}

// quoteArgs wraps each arg in double quotes for interpolation into a
// PowerShell command line built via -Command; values here are either
// fixed strings (see addVpnConnectionArgs) or the profile's own Gateway,
// never raw user free-text that could break out of the quoting.
func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = fmt.Sprintf("%q", a)
	}
	return out
}
