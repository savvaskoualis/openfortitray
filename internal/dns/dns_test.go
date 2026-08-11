package dns

import "testing"

// A representative `scutil --dns` dump on a mac where Tailscale MagicDNS owns the
// primary resolver (resolver #1, en0 → 100.100.100.100) while an openconnect VPN
// tunnel (utun4) has pushed the corp DNS 192.0.2.53 for corp.example. The
// discovery must ignore MagicDNS and pick the tunnel's resolver.
const scutilTailscalePlusVPN = `DNS configuration

resolver #1
  search domain[0] : lan
  nameserver[0] : 100.100.100.100
  if_index : 14 (en0)
  flags    : Request A records, Request AAAA records
  reach    : 0x00020002 (Reachable,Directly Reachable Address)

resolver #2
  domain   : corp.example
  nameserver[0] : 192.0.2.53
  if_index : 18 (utun4)
  flags    : Supplemental, Request A records
  reach    : 0x00000002 (Reachable)
  order    : 101400

DNS configuration (for scoped queries)

resolver #1
  nameserver[0] : 100.100.100.100
  if_index : 15 (utun3)
  flags    : Scoped, Request A records
  reach    : 0x00020002 (Reachable,Directly Reachable Address)

resolver #2
  nameserver[0] : 192.0.2.53
  if_index : 18 (utun4)
  flags    : Scoped, Request A records
  reach    : 0x00000002 (Reachable)
`

func TestPickVPNResolverTailscaleCoexistence(t *testing.T) {
	blocks := parseResolvers(scutilTailscalePlusVPN)
	// The domain hint is the decisive signal: corp.example is pushed by the
	// VPN, so resolver #2 in the first section wins even though Tailscale also
	// owns a utun.
	if got := pickVPNResolver(blocks, []string{"corp.example"}); got != "192.0.2.53" {
		t.Errorf("with a matching hint, picked %q, want 192.0.2.53", got)
	}
	// With no hint the tunnel-scoped fallback must still skip MagicDNS (utun3)
	// and land on the corp resolver.
	if got := pickVPNResolver(blocks, nil); got != "192.0.2.53" {
		t.Errorf("with no hint, picked %q, want 192.0.2.53 (MagicDNS must be skipped)", got)
	}
}

func TestPickVPNResolverSubdomainHintMatchesPushedApex(t *testing.T) {
	// The VPN pushes the apex "corp.example"; the profile lists a subdomain.
	blocks := parseResolvers(scutilTailscalePlusVPN)
	if got := pickVPNResolver(blocks, []string{"db.corp.example"}); got != "192.0.2.53" {
		t.Errorf("subdomain hint picked %q, want 192.0.2.53", got)
	}
}

func TestPickVPNResolverNoTunnelReturnsEmpty(t *testing.T) {
	// Only the ISP resolver on en0: nothing looks VPN-pushed.
	const onlyEn0 = `DNS configuration

resolver #1
  nameserver[0] : 192.168.1.1
  if_index : 14 (en0)
  flags    : Request A records
`
	if got := pickVPNResolver(parseResolvers(onlyEn0), []string{"corp.private"}); got != "" {
		t.Errorf("picked %q from a non-VPN config, want \"\"", got)
	}
}

func TestPickVPNResolverSkipsLoopbackAndMagicDNS(t *testing.T) {
	blocks := []resolverBlock{
		{iface: "utun0", nameserver: "127.0.0.1"},
		{iface: "utun1", nameserver: TailscaleMagicDNS},
		{iface: "utun2", nameserver: "10.20.30.40"},
	}
	if got := pickVPNResolver(blocks, nil); got != "10.20.30.40" {
		t.Errorf("picked %q, want 10.20.30.40 (loopback and MagicDNS must be skipped)", got)
	}
}

func TestParseResolversFields(t *testing.T) {
	blocks := parseResolvers(scutilTailscalePlusVPN)
	if len(blocks) != 4 {
		t.Fatalf("parsed %d resolver blocks, want 4", len(blocks))
	}
	// First block: en0 / MagicDNS / search "lan".
	if b := blocks[0]; b.iface != "en0" || b.nameserver != "100.100.100.100" {
		t.Errorf("block 0 = %+v, want en0 / 100.100.100.100", b)
	}
	if b := blocks[1]; b.iface != "utun4" || b.nameserver != "192.0.2.53" ||
		len(b.domains) != 1 || b.domains[0] != "corp.example" {
		t.Errorf("block 1 = %+v, want utun4 / 192.0.2.53 / [corp.example]", b)
	}
}
