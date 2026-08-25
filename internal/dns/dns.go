// Package dns discovers the DNS server a connected VPN pushed, so the app can
// install macOS per-domain scoped resolvers (/etc/resolver/<domain>) pointing at
// it. Those scoped resolvers make corp-internal names resolve through the VPN's
// DNS even when a global override — Tailscale MagicDNS at 100.100.100.100, a
// corporate DNS proxy — has taken over the system's primary resolver.
//
// Discovery is deliberately split into a pure parser (parseResolvers /
// pickVPNResolver, exercised by tests on every platform) and a thin
// platform-specific Discover that shells out to the OS. On macOS Discover reads
// `scutil --dns`; elsewhere it reports the mechanism is not implemented.
package dns

import (
	"errors"
	"regexp"
	"strings"
)

// ErrUnsupported means scoped-resolver DNS discovery is not implemented on this
// platform (see the Linux TODO in discover_other.go).
var ErrUnsupported = errors.New("dns: scoped-resolver discovery unsupported on this platform")

// ErrNoDNS means scutil reported no resolver that looks like a VPN-pushed one.
var ErrNoDNS = errors.New("dns: no VPN-pushed resolver found")

// TailscaleMagicDNS is Tailscale's well-known MagicDNS address. When Tailscale is
// up it appears as a resolver too, but it is never the corp DNS we want scoped
// resolvers to point at, so discovery skips it. Documented convenience, not a
// security boundary.
const TailscaleMagicDNS = "100.100.100.100"

// resolverBlock is one "resolver #N" stanza from `scutil --dns`.
type resolverBlock struct {
	iface      string   // interface from "if_index : N (utunX)"; "" if absent
	nameserver string   // the first "nameserver[N] : X"; "" if absent
	domains    []string // "domain :" and "search domain[N] :" values
}

var (
	reResolver   = regexp.MustCompile(`^resolver #\d+`)
	reNameserver = regexp.MustCompile(`^\s*nameserver\[\d+\]\s*:\s*(\S+)`)
	reIfIndex    = regexp.MustCompile(`^\s*if_index\s*:\s*\d+\s*\(([^)]+)\)`)
	reDomain     = regexp.MustCompile(`^\s*(?:search domain\[\d+\]|domain)\s*:\s*(\S+)`)
)

// parseResolvers splits `scutil --dns` output into resolver blocks. Section
// headers ("DNS configuration", "DNS configuration (for scoped queries)") do not
// start a block; only "resolver #N" lines do.
func parseResolvers(output string) []resolverBlock {
	var blocks []resolverBlock
	var cur *resolverBlock
	for _, line := range strings.Split(output, "\n") {
		if reResolver.MatchString(line) {
			blocks = append(blocks, resolverBlock{})
			cur = &blocks[len(blocks)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if m := reNameserver.FindStringSubmatch(line); m != nil {
			if cur.nameserver == "" { // keep only the first nameserver
				cur.nameserver = m[1]
			}
			continue
		}
		if m := reIfIndex.FindStringSubmatch(line); m != nil {
			cur.iface = m[1]
			continue
		}
		if m := reDomain.FindStringSubmatch(line); m != nil {
			cur.domains = append(cur.domains, m[1])
		}
	}
	return blocks
}

// isTunnelIface reports whether name is a VPN tunnel interface. openconnect's
// vpnc-script installs its resolver on a utun* device on macOS.
func isTunnelIface(name string) bool {
	return strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "ppp")
}

// skipNameserver reports whether ns is a resolver we must never treat as the
// corp DNS: the empty string, a loopback address, or Tailscale's MagicDNS (which
// is precisely the global override we are working around). Tailscale runs on a
// utun of its own, so this is what keeps discovery from picking it.
func skipNameserver(ns string) bool {
	switch {
	case ns == "":
		return true
	case ns == TailscaleMagicDNS:
		return true
	case strings.HasPrefix(ns, "127."):
		return true
	case ns == "::1":
		return true
	}
	return false
}

// domainMatches reports whether a resolver advertising resolverDomain is a match
// for one of the split-DNS hint domains — exact, or a parent of the hint (the
// VPN commonly pushes the apex "corp.private" for a hint of "svc.corp.private").
func domainMatches(resolverDomain string, hints []string) bool {
	rd := strings.TrimSuffix(strings.ToLower(resolverDomain), ".")
	for _, h := range hints {
		h = strings.TrimSuffix(strings.ToLower(h), ".")
		if h == "" {
			continue
		}
		if rd == h || strings.HasSuffix(h, "."+rd) || strings.HasSuffix(rd, "."+h) {
			return true
		}
	}
	return false
}

// pickVPNResolver returns the nameserver of the resolver most likely to be the
// VPN-pushed DNS. The order of preference:
//
//  1. A resolver advertising one of the hint (split-DNS) domains — the VPN
//     pushes its own search domain, so this is a definitive match and it also
//     disambiguates the case where both openconnect and Tailscale have a utun.
//  2. A resolver scoped to a tunnel interface (utun*/tun*/ppp*).
//
// In both cases known non-corp resolvers (Tailscale MagicDNS, loopback) are
// skipped. Returns "" when nothing qualifies.
func pickVPNResolver(blocks []resolverBlock, hints []string) string {
	for _, b := range blocks {
		if skipNameserver(b.nameserver) {
			continue
		}
		for _, d := range b.domains {
			if domainMatches(d, hints) {
				return b.nameserver
			}
		}
	}
	for _, b := range blocks {
		if skipNameserver(b.nameserver) {
			continue
		}
		if isTunnelIface(b.iface) {
			return b.nameserver
		}
	}
	return ""
}
