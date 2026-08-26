//go:build windows

package main

import (
	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/ipsec"
)

// newIPsecRunFunc builds the IPsec RunFunc for Windows: the native IKEv2
// VpnConnection cmdlets. See internal/ipsec.NewWindowsRunFunc. Certificate
// auth only — a PSK-auth profile is refused by NewWindowsRunFunc itself.
func newIPsecRunFunc(p config.Profile, psk string) ipsec.RunFunc {
	return ipsec.NewWindowsRunFunc(p, psk)
}
