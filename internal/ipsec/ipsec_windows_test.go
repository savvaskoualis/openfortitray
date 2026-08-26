//go:build windows

package ipsec

import (
	"testing"

	"github.com/savvaskoualis/openfortitray/internal/config"
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

func TestAddVpnConnectionArgsIncludeProfileFields(t *testing.T) {
	prof := testIPsecProfile()
	args := addVpnConnectionArgs(prof)
	for _, want := range []string{
		vpnConnectionName,
		"-ServerAddress", prof.Gateway,
		"-TunnelType", "IKEv2",
		"-AuthenticationMethod",
	} {
		if !argsContain(args, want) {
			t.Errorf("Add-VpnConnection args missing %q: %v", want, args)
		}
	}
}

func argsContain(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// testIPsecProfile mirrors strongswan_unix_test.go's helper of the same
// name: that file is darwin||linux-tagged, so this windows-tagged test
// file needs its own copy rather than sharing it.
func testIPsecProfile() config.Profile {
	return config.Profile{
		Name:    "Test",
		Gateway: "vpn.example.com",
		IPsec: config.IPsecConfig{
			AuthMethod:  config.IPsecAuthPSK,
			RemoteID:    "vpn.example.com",
			IKEProposal: "aes256-sha256-modp2048",
			ESPProposal: "aes256-sha256-modp2048",
		},
	}
}
