//go:build darwin

package dns

import (
	"context"
	"fmt"
	"os/exec"
)

// Discover reads `scutil --dns` and returns the DNS server the VPN pushed — the
// nameserver of the resolver scoped to the tunnel interface (or advertising one
// of hintDomains). hintDomains is the profile's split-DNS list; it is only a
// hint for picking the right resolver and may be empty. Returns ErrNoDNS when no
// candidate resolver is present yet (the caller retries: vpnc-script installs
// the resolver a moment after the tunnel reports up).
func Discover(ctx context.Context, hintDomains []string) (string, error) {
	out, err := exec.CommandContext(ctx, "scutil", "--dns").Output()
	if err != nil {
		return "", fmt.Errorf("dns: scutil --dns: %w", err)
	}
	ip := pickVPNResolver(parseResolvers(string(out)), hintDomains)
	if ip == "" {
		return "", ErrNoDNS
	}
	return ip, nil
}
