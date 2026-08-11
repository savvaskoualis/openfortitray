// Package settings renders the native settings window (profile list + Basic
// tab + action strip) on fyne v2 and holds the pure validation / working-copy
// logic that drives it.
//
// The window itself cannot be exercised without a display, so every decision it
// makes — field validation, unique/non-empty names, refuse-delete-last, the
// custom-port-off rule, and cloning the config into an editable working copy —
// lives in this file as a pure function and is table-tested in logic_test.go.
// The window (settings.go) is a thin shell that wires widgets to these.
package settings

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"

	"fyne.io/fyne/v2/data/validation"
)

// statusKind selects the colour of the bottom-strip status text. The mapping to
// an actual colour lives in settings.go so this stays pure and testable.
type statusKind int

const (
	statusGray   statusKind = iota // Disconnected
	statusYellow                   // Authenticating/Connecting/Reconnecting
	statusGreen                    // Connected
	statusRed                      // Error (terminal)
)

// statusFor maps a tunnel event onto what the window's status strip shows. It
// mirrors the tray's viewFor exactly: active reports whether the tunnel is up
// enough that Disconnect (not Connect) should be enabled — true for the
// connecting/connected states, false for Disconnected and the terminal Error
// (where Connect must be clickable again). Kept pure — no widgets, no colour —
// so it is table-tested alongside the tray's mapping.
func statusFor(e tunnel.Event) (text string, kind statusKind, active bool) {
	switch e.State {
	case tunnel.Connected:
		text = "Connected"
		if d := firstLine(e.Detail); d != "" {
			text += " — " + d
		}
		return text, statusGreen, true
	case tunnel.Authenticating, tunnel.Connecting, tunnel.Reconnecting:
		text = e.State.String() + "…"
		if d := firstLine(e.Detail); d != "" {
			text = e.State.String() + " — " + d
		}
		return text, statusYellow, true
	case tunnel.Error:
		text = "Error"
		if d := firstLine(e.Detail); d != "" {
			text = "Error: " + d
		}
		return text, statusRed, false
	default:
		return "Disconnected", statusGray, false
	}
}

// firstLine reduces multi-line event detail (openconnect can wrap errors over
// many lines) to its first non-empty-trimmed line, so the status strip stays a
// single line.
func firstLine(detail string) string {
	if i := strings.IndexAny(detail, "\r\n"); i >= 0 {
		detail = detail[:i]
	}
	return strings.TrimSpace(detail)
}

// defaultPort is the FortiGate SSL-VPN port a profile uses when "Use custom
// port" is off. It mirrors config.defaultProfile's Port.
const defaultPort = 10443

// Auth-method labels shown in the Authentication Select. Only SAML is wired
// into the runtime today (internal/auth); the other two are rendered so the
// config shape is forward-designed, but choosing one shows a "(not yet
// supported)" note (full gating lands with the Advanced tab, Fyne task 4).
const (
	authSAMLLabel = "SAML / SSO (external browser)"
	authPassLabel = "Username + password"
	authCertLabel = "Client certificate"
)

// authLabels is the Select's option list, in display order.
var authLabels = []string{authSAMLLabel, authPassLabel, authCertLabel}

// authLabel maps a stored method to its Select label. Unknown methods fall back
// to SAML so a hand-edited config never leaves the Select blank.
func authLabel(m config.AuthMethod) string {
	switch m {
	case config.AuthPassword:
		return authPassLabel
	case config.AuthCert:
		return authCertLabel
	default:
		return authSAMLLabel
	}
}

// authMethod maps a Select label back to a stored method.
func authMethod(label string) config.AuthMethod {
	switch label {
	case authPassLabel:
		return config.AuthPassword
	case authCertLabel:
		return config.AuthCert
	default:
		return config.AuthSAML
	}
}

// hostRe matches a bare host with no scheme and no port. It mirrors the
// installer's validate_gateway host rule.
var hostRe = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

// hostValidator is the fyne validator for the Gateway entry. An empty value is
// accepted (an unconfigured profile is savable; the empty-gateway guard in
// cmd/openfortitray refuses to dial it), so this validator only rejects a
// non-empty value that carries a scheme or port.
func hostValidator() func(string) error {
	re := validation.NewRegexp(hostRe.String(),
		"host only, no https:// or :port (e.g. vpn.example.com)")
	return func(s string) error {
		if s == "" {
			return nil
		}
		return re(s)
	}
}

// validateHost is the pure form of hostValidator, used by validateProfile so
// Save re-checks the whole working copy independently of any live widget state.
func validateHost(s string) error {
	if s == "" {
		return nil
	}
	if !hostRe.MatchString(s) {
		return errors.New("host only, no https:// or :port (e.g. vpn.example.com)")
	}
	return nil
}

// validatePortString validates the Port entry's raw text.
func validatePortString(s string) error {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return errors.New("port must be a whole number")
	}
	return validatePortValue(n)
}

// validatePortValue checks a port is in the usable range.
func validatePortValue(n int) error {
	if n < 1 || n > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

// validateName checks a profile name is non-empty and unique among the other
// profiles. self is the index of the profile being named, excluded from the
// uniqueness check so a profile does not collide with itself.
func validateName(name string, profiles []config.Profile, self int) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	for i := range profiles {
		if i == self {
			continue
		}
		if profiles[i].Name == name {
			return errors.New("a profile with this name already exists")
		}
	}
	return nil
}

// canDeleteProfile reports whether a profile may be removed given the current
// count. The last remaining profile may never be deleted: a config with no
// profile has nothing for Active() to return but a synthesised empty default,
// and the UI would have nothing to edit.
func canDeleteProfile(count int) bool {
	return count > 1
}

// effectivePort applies the custom-port-off rule: with the "Use custom port"
// check off, a profile always uses defaultPort regardless of what the (disabled)
// Port entry last held.
func effectivePort(customPort bool, port int) int {
	if !customPort {
		return defaultPort
	}
	return port
}

// validateProfile validates one profile against its siblings for Save. The port
// is only range-checked when a custom port is in use (otherwise it is forced to
// defaultPort by effectivePort and cannot be wrong).
func validateProfile(p config.Profile, all []config.Profile, self int) error {
	if err := validateName(p.Name, all, self); err != nil {
		return err
	}
	if err := validateHost(p.Gateway); err != nil {
		return err
	}
	if p.CustomPort {
		if err := validatePortValue(p.Port); err != nil {
			return err
		}
	}
	return nil
}

// validateConfig validates the whole working copy for Save: at least one
// profile, and every profile individually valid.
func validateConfig(c *config.Config) error {
	if len(c.Profiles) == 0 {
		return errors.New("at least one profile is required")
	}
	for i := range c.Profiles {
		if err := validateProfile(c.Profiles[i], c.Profiles, i); err != nil {
			name := c.Profiles[i].Name
			if name == "" {
				return fmt.Errorf("profile %d: %w", i+1, err)
			}
			return fmt.Errorf("profile %q: %w", name, err)
		}
	}
	return nil
}

// uniqueName returns base if no profile already uses it, otherwise base with a
// numeric suffix (" 2", " 3", …) until it is free. Used by Add and Duplicate so
// a new profile never violates the unique-name rule on creation.
func uniqueName(base string, profiles []config.Profile) string {
	taken := func(name string) bool {
		for i := range profiles {
			if profiles[i].Name == name {
				return true
			}
		}
		return false
	}
	if !taken(base) {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s %d", base, n)
		if !taken(candidate) {
			return candidate
		}
	}
}

// cloneConfig deep-copies a config so the settings window can edit a working
// copy without touching the live config until Save/Commit. The only reference
// types inside a Profile are the SplitDNS slice, copied element-wise.
func cloneConfig(c *config.Config) *config.Config {
	out := *c
	out.Profiles = make([]config.Profile, len(c.Profiles))
	for i := range c.Profiles {
		p := c.Profiles[i]
		if c.Profiles[i].SplitDNS != nil {
			p.SplitDNS = append([]string(nil), c.Profiles[i].SplitDNS...)
		}
		out.Profiles[i] = p
	}
	return &out
}

// renameProfile sets profile sel's name and, if that profile was the active
// one, moves ActiveProfile onto the new name so the active pointer tracks the
// rename. Without this, renaming the active profile orphans ActiveProfile:
// the "● " marker vanishes and, after Save, config.Active() silently falls back
// to Profiles[0] — dialing the wrong profile whenever the renamed one is not
// first.
func renameProfile(c *config.Config, sel int, newName string) {
	old := c.Profiles[sel].Name
	c.Profiles[sel].Name = newName
	if c.ActiveProfile == old {
		c.ActiveProfile = newName
	}
}

// parsePort parses the Port entry's raw text into an integer.
func parsePort(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}

// itoa renders a port back to text for the Port entry.
func itoa(n int) string {
	return strconv.Itoa(n)
}

// normalizePorts applies effectivePort across every profile, so a saved config
// never carries a stale custom port for a profile whose custom-port check is
// off. Save calls this just before validation and commit.
func normalizePorts(c *config.Config) {
	for i := range c.Profiles {
		c.Profiles[i].Port = effectivePort(c.Profiles[i].CustomPort, c.Profiles[i].Port)
	}
}
