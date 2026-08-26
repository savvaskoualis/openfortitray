//go:build windows

package ipsec

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
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

// vpnConnArg is one Add-VpnConnection flag and its value. Kept as a
// flag/value pair — rather than a flat []string — specifically so the
// flag token and its value are never confused when rendering the command
// line: PowerShell only recognizes an UNQUOTED -Flag token as a parameter
// designator; a quoted "-Flag" is parsed as a plain string literal and
// falls through to positional binding instead. Blanket-quoting a flat
// []string (as an earlier version of this file did) silently breaks
// every Add-VpnConnection call for exactly this reason.
type vpnConnArg struct {
	flag  string
	value string
}

// addVpnConnectionArgs builds Add-VpnConnection's flag/value pairs for p.
// Windows IKEv2 in this app supports certificate authentication ONLY —
// see NewWindowsRunFunc, which refuses PSK profiles before ever calling
// here. Add-VpnConnection's -AuthenticationMethod ValidateSet is
// Pap/Chap/MSChapv2/Eap/MachineCertificate (verified against Microsoft's
// documented parameter reference): "PSK" is not a member under any
// casing, and -L2tpPsk is documented for L2TP authentication only, not
// IKEv2 — there is no supported Add-VpnConnection path for IKEv2+PSK, so
// this always emits MachineCertificate.
func addVpnConnectionArgs(p config.Profile) []vpnConnArg {
	return []vpnConnArg{
		{"-Name", vpnConnectionName},
		{"-ServerAddress", p.Gateway},
		{"-TunnelType", "IKEv2"},
		{"-AuthenticationMethod", "MachineCertificate"},
		{"-EncryptionLevel", "Required"},
	}
}

// renderAddVpnConnectionCmd renders args into an Add-VpnConnection
// PowerShell command line: flag tokens are emitted bare and only values
// are double-quoted, preserving the distinction vpnConnArg's doc comment
// describes. -Force is a switch parameter (no value), appended bare.
func renderAddVpnConnectionCmd(args []vpnConnArg) string {
	parts := make([]string, 0, len(args)*2+2)
	for _, a := range args {
		parts = append(parts, a.flag, fmt.Sprintf("%q", a.value))
	}
	parts = append(parts, "-Force")
	return "Add-VpnConnection " + strings.Join(parts, " ")
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
// VPN stack for profile. psk is accepted for interface parity with
// NewStrongSwanRunFunc (Task 6 calls both platforms' constructors
// uniformly) but is otherwise unused here: Windows IKEv2 in this app only
// supports certificate authentication (see addVpnConnectionArgs), so a
// PSK-auth profile is refused immediately below rather than attempted —
// there is no documented Add-VpnConnection path for IKEv2+PSK, and
// passing a secret as a plaintext PowerShell command-line argument would
// also be visible via process enumeration / Security Event Log auditing.
func NewWindowsRunFunc(p config.Profile, psk string) RunFunc {
	_ = psk
	return func(ctx context.Context, connected func(ip string)) error {
		if p.IPsec.AuthMethod == config.IPsecAuthPSK {
			// Deterministic: no Add-VpnConnection call this file could make would
			// ever accept PSK, so retrying is pointless — see addVpnConnectionArgs.
			// PSK is also the default IPsecAuthMethod for a new profile, so this is
			// a common way to reach here, not an edge case.
			return fmt.Errorf("%w: PSK auth is not supported for IPsec on Windows — use a certificate, or connect from macOS/Linux",
				tunnel.ErrPermanent)
		}

		cmdline := fmt.Sprintf("Remove-VpnConnection -Name %q -Force -ErrorAction SilentlyContinue; %s",
			vpnConnectionName, renderAddVpnConnectionCmd(addVpnConnectionArgs(p)))
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
						// ClientIPAddress is assumed, not confirmed: it does
						// not appear in Microsoft's documented example
						// Get-VpnConnection output. Best-effort — an error
						// here still calls connected() with an empty ip
						// rather than failing the whole connection.
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
