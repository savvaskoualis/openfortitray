// Package settings' bridge.go is the one file a UI layer (Wails' Bridge, or
// any future one) calls into. Everything it wraps already exists in
// logic.go, unexported because until now only this package's own Qt render
// layer called it.
package settings

import "github.com/savvaskoualis/openfortitray/internal/config"

// Host is implemented by the app adapter (cmd/openfortitray/main.go) and is
// the only way this package's consumers reach live configuration. Connect/
// Disconnect were dropped from this interface (nothing in this package calls
// them through it — app's real Connect/Disconnect are reached directly, not
// via settings.Host) rather than left dead.
type Host interface {
	Config() *config.Config
	Commit(c *config.Config) error
}

// Validate runs the same checks Commit always ran internally, exposed for a
// UI layer to call before committing so it can show a field-level error
// instead of a generic failure. Returns nil when c is valid.
func Validate(c *config.Config) *Issue {
	if err := validateConfig(c); err != nil {
		return &Issue{Message: err.Error()}
	}
	return nil
}

// Controller retains the one piece of the deleted Qt render layer that is
// actually framework-agnostic dirty-tracking logic rather than widget
// wiring: ipsecSecretDirty/ipsecSecretValue track an unsaved PSK edit keyed
// by profile index, and reindexIPsecSecrets keeps both maps aligned with
// c.work.Profiles after a profile is deleted. logic_test.go
// (TestReindexIPsecSecretsAfterDelete, TestReindexIPsecSecretsAfterDeleteFirst)
// exercises this directly against a bare Controller and predates this task,
// so it is kept here — unchanged — rather than dropped with the rest of the
// old settings.go.
type Controller struct {
	ipsecSecretDirty map[int]bool
	ipsecSecretValue map[int]string
}

// reindexIPsecSecrets drops the not-yet-saved PSK edit (if any) for the
// profile at index removed, and shifts every later index down by one, so
// ipsecSecretDirty/ipsecSecretValue — keyed by profile index — stay aligned
// with c.work.Profiles after deleteProfile shifts everything after removed
// left by one.
func (c *Controller) reindexIPsecSecrets(removed int) {
	dirty := map[int]bool{}
	value := map[int]string{}
	for idx, v := range c.ipsecSecretDirty {
		switch {
		case idx == removed:
			continue
		case idx > removed:
			dirty[idx-1] = v
		default:
			dirty[idx] = v
		}
	}
	for idx, v := range c.ipsecSecretValue {
		switch {
		case idx == removed:
			continue
		case idx > removed:
			value[idx-1] = v
		default:
			value[idx] = v
		}
	}
	c.ipsecSecretDirty = dirty
	c.ipsecSecretValue = value
}
