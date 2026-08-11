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

// Server-certificate mode labels shown in the Advanced tab's RadioGroup.
const (
	certWarnLabel  = "Warn on invalid"
	certTrustLabel = "Trust (accept invalid)"
	certPinLabel   = "Pin fingerprint"
)

// certModeLabels is the RadioGroup's option list, in display order.
var certModeLabels = []string{certWarnLabel, certTrustLabel, certPinLabel}

// certModeLabel maps a stored server-certificate mode to its RadioGroup label.
// An unknown/empty mode falls back to warn (the safe default).
func certModeLabel(m config.ServerCertMode) string {
	switch m {
	case config.CertTrust:
		return certTrustLabel
	case config.CertPin:
		return certPinLabel
	default:
		return certWarnLabel
	}
}

// certMode maps a RadioGroup label back to a stored mode.
func certMode(label string) config.ServerCertMode {
	switch label {
	case certTrustLabel:
		return config.CertTrust
	case certPinLabel:
		return config.CertPin
	default:
		return config.CertWarn
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

// fingerprintRe matches a server-certificate fingerprint: hex digits and the
// ':' byte separators openconnect prints (e.g. sha256:AB:CD:… or a bare hex
// string). It intentionally does not enforce a length, so both SHA-1 and
// SHA-256 forms and openconnect's "sha256:" prefixless hex are accepted.
var fingerprintRe = regexp.MustCompile(`^[A-Fa-f0-9:]+$`)

// validateFingerprint validates a pinned certificate fingerprint. It is
// required (non-empty) only when the server-certificate mode is Pin; the live
// entry validator (fingerprintCharset) accepts empty so the form is not blocked
// while another mode is selected, and this Save-time check enforces presence.
func validateFingerprint(s string) error {
	if s == "" {
		return errors.New("a fingerprint is required when pinning the server certificate")
	}
	if !fingerprintRe.MatchString(s) {
		return errors.New("fingerprint may contain only hex digits (0-9 a-f) and ':'")
	}
	return nil
}

// fingerprintCharset is the live entry validator for the pin field: it enforces
// the charset but tolerates empty, because fyne's Form.Validate() runs every
// entry's validator regardless of whether its mode is selected, and a required
// error on a field the user is not using would wedge the whole form.
func fingerprintCharset(s string) error {
	if s == "" {
		return nil
	}
	if !fingerprintRe.MatchString(s) {
		return errors.New("fingerprint may contain only hex digits (0-9 a-f) and ':'")
	}
	return nil
}

// domainRe matches a single DNS domain (one or more labels, each 1-63 chars of
// letters/digits/hyphen, not starting or ending in a hyphen). Single-label names
// like "internal" are allowed — split-DNS routinely scopes such suffixes.
var domainRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// validateDomain checks one split-DNS domain.
func validateDomain(s string) error {
	if s == "" {
		return errors.New("domain is empty")
	}
	if len(s) > 253 {
		return errors.New("domain is too long")
	}
	if !domainRe.MatchString(s) {
		return errors.New("not a valid domain (e.g. corp.example.com)")
	}
	return nil
}

// parseSplitDNS turns the multi-line split-DNS entry text into the []string the
// config stores: one trimmed, non-empty line per element. An all-blank field
// yields nil (no domains), which is valid.
func parseSplitDNS(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// validateSplitDNSText validates the raw multi-line entry: every non-empty line
// must be a valid domain; a wholly empty field is accepted.
func validateSplitDNSText(text string) error {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if err := validateDomain(line); err != nil {
			return fmt.Errorf("split-DNS domain %q: %w", line, err)
		}
	}
	return nil
}

// openconnectPathRe mirrors the installer's charset for the openconnect binary
// path: an absolute path or a bare name made of letters, digits and . _ / + -.
var openconnectPathRe = regexp.MustCompile(`^[A-Za-z0-9._/+-]+$`)

// validateOpenconnectPath validates the (top-level) openconnect binary path. It
// tolerates empty so a config that never set it still validates — Load supplies
// the "openconnect" default — and only rejects a non-empty value with stray
// characters.
func validateOpenconnectPath(s string) error {
	if s == "" {
		return nil
	}
	if !openconnectPathRe.MatchString(s) {
		return errors.New("path may contain only letters, digits and . _ / + -")
	}
	return nil
}

// openconnectPathEntryValidator is the live entry validator: unlike the Save
// check it requires a value, because the field always shows the resolved default
// and a user should not be able to blank it in the UI.
func openconnectPathEntryValidator(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("openconnect path is required")
	}
	return validateOpenconnectPath(s)
}

// validateAuthSupported gates Save on the auth method of the profile that will
// actually be dialed (the active one). Only SAML/SSO is wired into the runtime
// today (internal/auth); the other methods are forward-designed in the schema
// but have no Authenticator, so activating one would fail at connect time with
// an opaque error. Refuse it at Save with a message that names the fix.
func validateAuthSupported(c *config.Config) error {
	switch c.Active().Auth.Method {
	case config.AuthPassword:
		return errors.New("username/password auth not yet supported — use SAML/SSO")
	case config.AuthCert:
		return errors.New("client-certificate auth not yet supported — use SAML/SSO")
	default:
		// Empty normalizes to SAML elsewhere; treat unknown as allowed here.
		return nil
	}
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

// effectiveSAMLPort shows the default SAML redirect port when a profile has
// none stored yet (0), mirroring config's normalizeProfile default.
func effectiveSAMLPort(port int) int {
	if port == 0 {
		return defaultSAMLPort
	}
	return port
}

// defaultSAMLPort mirrors config.defaultProfile's SAMLPort.
const defaultSAMLPort = 8020

// effectiveOpenconnectPath shows the "openconnect" default when the top-level
// path is unset, mirroring config.defaults().
func effectiveOpenconnectPath(path string) string {
	if path == "" {
		return "openconnect"
	}
	return path
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
	// SAMLPort 0 means "unset → default 8020" (Load/normalize fill it); only a
	// genuinely out-of-range value is rejected, so minimally-populated profiles
	// still validate.
	if p.SAMLPort != 0 {
		if err := validatePortValue(p.SAMLPort); err != nil {
			return fmt.Errorf("SAML port: %w", err)
		}
	}
	if p.ServerCert.Mode == config.CertPin {
		if err := validateFingerprint(p.ServerCert.Pin); err != nil {
			return err
		}
	}
	for _, d := range p.SplitDNS {
		if err := validateDomain(d); err != nil {
			return fmt.Errorf("split-DNS domain %q: %w", d, err)
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
	if err := validateOpenconnectPath(c.OpenconnectPath); err != nil {
		return fmt.Errorf("openconnect path: %w", err)
	}
	// The active profile is the one that will be dialed; refuse to save a config
	// that would try to use an auth method with no runtime behind it.
	if err := validateAuthSupported(c); err != nil {
		return err
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
