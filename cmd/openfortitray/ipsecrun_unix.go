//go:build darwin || linux

package main

import (
	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/ipsec"
)

// newIPsecRunFunc builds the IPsec RunFunc for macOS/Linux: strongSwan driven
// via swanctl. See internal/ipsec.NewStrongSwanRunFunc.
func newIPsecRunFunc(p config.Profile, psk string) ipsec.RunFunc {
	return ipsec.NewStrongSwanRunFunc(p, psk)
}
