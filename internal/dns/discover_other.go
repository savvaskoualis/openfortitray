//go:build !darwin

package dns

import "context"

// Discover is not implemented off macOS.
//
// TODO(linux-splitdns): Linux does not use /etc/resolver. Split-DNS there is a
// systemd-resolved job — `resolvectl domain <iface> ~<domain>` to route a domain
// to a link, plus `resolvectl dns <iface> <server>` for that link's DNS — driven
// off the tunnel interface name. Until that is built and tested, Discover
// reports the mechanism is unsupported so the caller installs no scoped
// resolvers rather than writing files that do nothing (cmd/openfortitray also
// gates split-DNS to darwin, so this path is not reached in production today).
func Discover(ctx context.Context, hintDomains []string) (string, error) {
	return "", ErrUnsupported
}
