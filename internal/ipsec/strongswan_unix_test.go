//go:build darwin || linux

package ipsec

import (
	"strings"
	"testing"

	"github.com/savvaskoualis/openfortitray/internal/config"
)

func TestParseCharonLineExtractsAssignedIP(t *testing.T) {
	line := `CHILD_SA oft-tun{1} established with SPIs c1234567_i c89abcde_o and TS 0.0.0.0/0 === 10.212.140.5/32`
	ip, established, failed := parseCharonLine(line)
	if !established || failed {
		t.Fatalf("established=%v failed=%v, want established=true failed=false", established, failed)
	}
	if ip != "10.212.140.5" {
		t.Errorf("ip = %q, want %q", ip, "10.212.140.5")
	}
}

func TestParseCharonLineDetectsFailure(t *testing.T) {
	// "initiate failed: %s" is swanctl's own CLI summary line (verified
	// against the compiled swanctl 6.0.7 binary via `strings`), printed
	// unconditionally to the same stream this code scans when --initiate
	// doesn't succeed — unlike a single fixed charon log line, which
	// doesn't exist for this case (the underlying reason varies: peer not
	// responding, no proposal chosen, auth failed, ...).
	_, established, failed := parseCharonLine(`initiate failed: establishing CHILD_SA failed`)
	if established || !failed {
		t.Fatalf("established=%v failed=%v, want established=false failed=true", established, failed)
	}
}

func TestParseCharonLineIgnoresUnrelatedLines(t *testing.T) {
	ip, established, failed := parseCharonLine("received packet: from 1.2.3.4[500]")
	if ip != "" || established || failed {
		t.Errorf("got ip=%q established=%v failed=%v, want all zero/false", ip, established, failed)
	}
}

func TestSwanctlConnFragmentIncludesProfileFields(t *testing.T) {
	prof := testIPsecProfile()
	frag := swanctlConnFragment(prof)
	for _, want := range []string{
		"oft-tun",
		prof.Gateway,
		prof.IPsec.RemoteID,
		prof.IPsec.IKEProposal,
		prof.IPsec.ESPProposal,
		"version = 2", // IKEv2 only
	} {
		if !containsString(frag, want) {
			t.Errorf("swanctl fragment missing %q:\n%s", want, frag)
		}
	}
}

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

func containsString(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
