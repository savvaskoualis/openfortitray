//go:build windows

package ipsec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

func TestParseVpnConnectionStatusRecognizesEachState(t *testing.T) {
	cases := map[string]vpnStatus{
		"Connected":    vpnConnected,
		"Connecting":   vpnConnecting,
		"Disconnected": vpnDisconnected,
	}
	for input, want := range cases {
		if got := parseVpnConnectionStatus(input); got != want {
			t.Errorf("parseVpnConnectionStatus(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestParseVpnConnectionStatusUnknownIsDisconnected(t *testing.T) {
	if got := parseVpnConnectionStatus("SomethingNew"); got != vpnDisconnected {
		t.Errorf("unknown status = %v, want vpnDisconnected (fail closed, never hang assuming connected)", got)
	}
}

// TestAddVpnConnectionArgsIncludeProfileFields exercises the (and only
// supported) cert-auth shape: Windows IKEv2 in this app supports
// certificate authentication only, so addVpnConnectionArgs always emits
// MachineCertificate regardless of the profile's own AuthMethod — the PSK
// refusal happens earlier, in NewWindowsRunFunc (see
// TestNewWindowsRunFuncRefusesPSKWithoutCallingPowerShell below).
func TestAddVpnConnectionArgsIncludeProfileFields(t *testing.T) {
	prof := testIPsecProfile()
	args := addVpnConnectionArgs(prof)
	want := map[string]string{
		"-Name":                 vpnConnectionName,
		"-ServerAddress":        prof.Gateway,
		"-TunnelType":           "IKEv2",
		"-AuthenticationMethod": "MachineCertificate",
	}
	for flag, wantValue := range want {
		if !argsHasFlagValue(args, flag, wantValue) {
			t.Errorf("Add-VpnConnection args missing %s=%q: %+v", flag, wantValue, args)
		}
	}
}

// TestAddVpnConnectionArgsNeverQuotesFlagTokens guards against the exact
// regression this file previously shipped: quoting every element of a
// flat []string (flags included) makes PowerShell parse a quoted "-Flag"
// as a plain string literal rather than a parameter designator, so
// Add-VpnConnection degenerates into misaligned positional arguments and
// fails every call. vpnConnArg's flag field must always be the bare,
// unquoted token.
func TestAddVpnConnectionArgsNeverQuotesFlagTokens(t *testing.T) {
	for _, a := range addVpnConnectionArgs(testIPsecProfile()) {
		if strings.HasPrefix(a.flag, `"`) {
			t.Errorf("flag %q is quoted; PowerShell would parse it as a string literal, not a parameter designator", a.flag)
		}
		if !strings.HasPrefix(a.flag, "-") {
			t.Errorf("flag %q does not look like a PowerShell parameter designator", a.flag)
		}
	}
}

func argsHasFlagValue(args []vpnConnArg, flag, value string) bool {
	for _, a := range args {
		if a.flag == flag && a.value == value {
			return true
		}
	}
	return false
}

// TestNewWindowsRunFuncRefusesPSKWithoutCallingPowerShell confirms the
// PSK refusal happens before any exec.Command — asserted here by never
// needing powershell.exe to exist on this machine (there is none) for
// the returned RunFunc to fail with a clear, actionable error.
func TestNewWindowsRunFuncRefusesPSKWithoutCallingPowerShell(t *testing.T) {
	prof := config.Profile{
		Name:    "Test",
		Gateway: "vpn.example.com",
		IPsec: config.IPsecConfig{
			AuthMethod: config.IPsecAuthPSK,
		},
	}
	run := NewWindowsRunFunc(prof, "some-psk")

	err := run(context.Background(), func(ip string) {
		t.Fatal("connected callback must never be called for a refused PSK profile")
	})
	if err == nil {
		t.Fatal("want an error for PSK auth on Windows, got nil")
	}
	if !strings.Contains(err.Error(), "PSK") || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("error %q should clearly mention both PSK and certificate", err.Error())
	}
	// Deterministic: switching to PSK auth on Windows can never succeed by
	// retrying (Add-VpnConnection has no IKEv2+PSK path at all), and PSK is
	// the default IPsecAuthMethod for a new profile, so this must be wrapped
	// as tunnel.ErrPermanent (Important #2) rather than retried forever.
	if !errors.Is(err, tunnel.ErrPermanent) {
		t.Errorf("PSK-refusal error %v must wrap tunnel.ErrPermanent", err)
	}
}

// testIPsecProfile mirrors strongswan_unix_test.go's helper of the same
// name: that file is darwin||linux-tagged, so this windows-tagged test
// file needs its own copy rather than sharing it. Cert-based (not
// PSK-based): PSK is not a supported Windows IKEv2 auth method here, so a
// realistic profile for exercising addVpnConnectionArgs uses cert auth.
func testIPsecProfile() config.Profile {
	return config.Profile{
		Name:    "Test",
		Gateway: "vpn.example.com",
		IPsec: config.IPsecConfig{
			AuthMethod:  config.IPsecAuthCert,
			CertPath:    `C:\certs\client.pfx`,
			RemoteID:    "vpn.example.com",
			IKEProposal: "aes256-sha256-modp2048",
			ESPProposal: "aes256-sha256-modp2048",
		},
	}
}
