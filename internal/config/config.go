// Package config holds static VPN settings and user preferences.
//
// The on-disk format is versioned. schemaVersion 2/3 is the multi-profile shape
// below (v3 added Profile.RememberSession); a file with no schemaVersion (the
// original flat, single-connection layout) is a "legacy" file and is migrated in
// place on first load. See migrate for the exact rules.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// schemaVersion is the current on-disk config version. It is written by Save
// and drives migrate. v3 added Profile.RememberSession; a v2 file predates that
// field, so migrate backfills its default (true) rather than the JSON zero.
const schemaVersion = 3

// Config is the whole config.json. Per-connection settings live in Profiles;
// only machine-wide keys stay top-level.
type Config struct {
	// SchemaVersion is 0/absent for a legacy flat file and 2/3 for this shape.
	SchemaVersion int       `json:"schemaVersion"`
	ActiveProfile string    `json:"activeProfile"`
	Profiles      []Profile `json:"profiles"`

	// OpenconnectPath is only used where the app is already privileged
	// (Windows). On macOS/Linux the tunnel goes through HelperPath, which
	// resolves openconnect itself from a fixed PATH.
	OpenconnectPath string `json:"openconnect_path"`
	// HelperPath is the root-owned privileged helper installed by
	// scripts/install.sh; the sudoers rule is scoped to exactly this path, so
	// changing it here without reinstalling will make sudo ask for a password
	// and the tunnel will fail to start.
	HelperPath string `json:"helper_path"`
	// Autostart connects the active profile at launch.
	Autostart bool `json:"autostart"`
}

// AuthMethod selects how a profile authenticates to the gateway.
type AuthMethod string

const (
	// AuthSAML is SAML/SSO via an external browser. It is the only method
	// wired into the runtime today.
	AuthSAML AuthMethod = "saml"
	// AuthPassword is username+password. Designed in the schema, gated at the
	// runtime — not yet implemented.
	AuthPassword AuthMethod = "password"
	// AuthCert is client-certificate auth. Designed in the schema, gated at
	// the runtime — not yet implemented.
	AuthCert AuthMethod = "cert"
)

// Backend selects which VPN protocol a profile dials. AuthSSL (openconnect,
// wired into the runtime today) or AuthIPsec (strongSwan — schema only,
// not yet wired; see internal/tunnel and the IPsec design doc).
type Backend string

const (
	// BackendSSL is openconnect's FortiGate SSL-VPN protocol — the only
	// backend actually implemented today.
	BackendSSL Backend = "ssl"
	// BackendIPsec is FortiGate's IPsec remote-access mode. Forward-designed
	// in the schema; connecting with it is refused with a clear message
	// (see internal/settings' updateAuthNote/authNoteText) until the
	// strongSwan runtime exists.
	BackendIPsec Backend = "ipsec"
)

// AuthConfig carries the auth method and its non-secret parameters. A password
// is NEVER stored here; if password auth is ever implemented the secret goes to
// the OS keychain, not config.json.
type AuthConfig struct {
	Method   AuthMethod `json:"method"`
	Username string     `json:"username,omitempty"`
	CertPath string     `json:"cert_path,omitempty"`
}

// ServerCertMode selects how an invalid/unknown server certificate is handled.
type ServerCertMode string

const (
	// CertWarn warns on an invalid server certificate (the default).
	CertWarn ServerCertMode = "warn"
	// CertTrust accepts an invalid server certificate.
	CertTrust ServerCertMode = "trust"
	// CertPin trusts exactly the fingerprint in ServerCert.Pin.
	CertPin ServerCertMode = "pin"
)

// ServerCert configures server-certificate handling for a profile.
type ServerCert struct {
	Mode ServerCertMode `json:"mode"`
	Pin  string         `json:"pin,omitempty"` // sha256 fingerprint when Mode==CertPin
}

// Profile is a single VPN connection's settings.
type Profile struct {
	Name       string `json:"name"`
	Gateway    string `json:"gateway"`     // host, no scheme/port (empty-gateway guard applies)
	Port       int    `json:"port"`        // default 10443
	CustomPort bool   `json:"custom_port"` // FortiClient EnableCustomPort
	SAMLPort   int    `json:"saml_port"`   // default 8020

	Auth    AuthConfig `json:"auth"`
	Backend Backend    `json:"backend"`
	Realm   string     `json:"realm,omitempty"`

	DualStack  bool       `json:"dual_stack"`
	DTLS       bool       `json:"dtls"`       // default true (PreferDtlsTunnel)
	KeepAlive  bool       `json:"keep_alive"` // gate the supervisor's reconnect
	ServerCert ServerCert `json:"server_cert"`
	SplitDNS   []string   `json:"split_dns,omitempty"`
	Quiet      bool       `json:"quiet,omitempty"`

	// RememberSession lets the app reuse a stored SVPNCOOKIE (kept in
	// platform-native secret storage, never in this file) across reconnects and
	// restarts, falling back to the SAML browser flow only when the gateway
	// rejects it. Default true; turning it off never stores or reuses a cookie
	// and deletes any already stored for this profile's gateway. schemaVersion 3
	// backfills true for pre-v3 files (see migrate).
	RememberSession bool `json:"remember_session"`
}

// GatewayURL is the https base URL for this profile's gateway.
func (p *Profile) GatewayURL() string {
	return fmt.Sprintf("https://%s:%d", p.Gateway, p.Port)
}

// defaultProfile is a fresh profile named "Default" with an empty gateway (so
// the empty-gateway guard still trips) and every other field at its default.
func defaultProfile() Profile {
	return Profile{
		Name:            "Default",
		Gateway:         "",
		Port:            10443,
		SAMLPort:        8020,
		Auth:            AuthConfig{Method: AuthSAML},
		Backend:         BackendSSL,
		DTLS:            true,
		ServerCert:      ServerCert{Mode: CertWarn},
		RememberSession: true,
	}
}

// NewProfile returns a fresh profile with the given name and every other field
// at its default: an empty gateway (so the empty-gateway guard still trips),
// port 10443, SAML auth, DTLS on and warn-on-invalid-certificate. It is the
// single source of a "blank" profile, used by the settings UI's Add button so
// new profiles match what Load synthesises for a fresh install.
func NewProfile(name string) Profile {
	p := defaultProfile()
	p.Name = name
	return p
}

// normalizeProfile fills fields whose zero value is invalid with their default,
// so every profile returned by migrate is usable regardless of which keys the
// on-disk file supplied. It does not rely on json.Unmarshal reusing a
// pre-populated slice element, so it corrects profiles[1:] too.
//
// DTLS is a bool and cannot distinguish an omitted key from an explicit
// "dtls": false, so it is left as-is: a hand-edited v2 file that omits "dtls"
// reads as false. Save always writes the key, so files it round-trips are fine.
func normalizeProfile(p *Profile) {
	if p.Port == 0 {
		p.Port = 10443
	}
	if p.SAMLPort == 0 {
		p.SAMLPort = 8020
	}
	if p.Auth.Method == "" {
		p.Auth.Method = AuthSAML
	}
	if p.Backend == "" {
		p.Backend = BackendSSL
	}
	if p.ServerCert.Mode == "" {
		p.ServerCert.Mode = CertWarn
	}
}

func defaults() *Config {
	return &Config{
		SchemaVersion:   schemaVersion,
		ActiveProfile:   "Default",
		Profiles:        []Profile{defaultProfile()},
		OpenconnectPath: "openconnect",
		HelperPath:      "/usr/local/libexec/openfortitray-tunnel",
		Autostart:       true,
	}
}

// Active returns the profile named by ActiveProfile, falling back to the first
// profile, or a synthesized empty default. The empty default has Gateway=="",
// so a config with no usable profile still trips the empty-gateway guard in the
// caller rather than dialing a bare endpoint.
func (c *Config) Active() *Profile {
	for i := range c.Profiles {
		if c.Profiles[i].Name == c.ActiveProfile {
			return &c.Profiles[i]
		}
	}
	if len(c.Profiles) > 0 {
		return &c.Profiles[0]
	}
	p := defaultProfile()
	p.Gateway = ""
	return &p
}

// migrate turns raw config.json bytes into a *Config, reporting whether the
// input was a legacy file that was upgraded. It is pure: it neither reads nor
// writes the filesystem, so it is exercised directly by table-driven tests.
//
// Rules:
//   - schemaVersion >= 2: unmarshal as the current shape, overlaid onto
//     defaults so omitted top-level keys keep their defaults. upgraded=false.
//   - otherwise (legacy flat file, no schemaVersion): build a single profile
//     named "Default" from gateway/port/saml_port; openconnect_path,
//     helper_path and autostart stay top-level. gateway is copied verbatim,
//     including "", so an unconfigured install still yields an empty gateway.
//     upgraded=true.
//   - malformed JSON: error.
func migrate(raw []byte) (*Config, bool, error) {
	var probe struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, false, fmt.Errorf("parse config.json: %w", err)
	}

	if probe.SchemaVersion >= 2 {
		// Start from defaults for the top-level keys, but with an EMPTY Profiles
		// slice so json.Unmarshal cannot reuse a pre-populated Profiles[0] (which
		// would leave profiles[1:] with invalid zero values). Every profile is
		// normalized post-unmarshal instead.
		c := defaults()
		c.Profiles = nil
		if err := json.Unmarshal(raw, c); err != nil {
			return nil, false, fmt.Errorf("parse config.json: %w", err)
		}
		// remember_session (v3) defaults true, but JSON cannot tell an omitted key
		// from an explicit false. A file at schemaVersion < 3 predates the field, so
		// every profile in it wants the default; backfill true. A v3+ file is trusted
		// as written (Save always emits the key, so round-trips are exact).
		backfillRemember := probe.SchemaVersion < 3
		for i := range c.Profiles {
			normalizeProfile(&c.Profiles[i])
			if backfillRemember {
				c.Profiles[i].RememberSession = true
			}
		}
		return c, false, nil
	}

	// Legacy flat file. Pointers distinguish an absent key (keep the default)
	// from one explicitly set to a zero value (honour it, e.g. gateway:"").
	var legacy struct {
		Gateway         *string `json:"gateway"`
		Port            *int    `json:"port"`
		SAMLPort        *int    `json:"saml_port"`
		OpenconnectPath *string `json:"openconnect_path"`
		HelperPath      *string `json:"helper_path"`
		Autostart       *bool   `json:"autostart"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, false, fmt.Errorf("parse config.json: %w", err)
	}

	c := defaults()
	p := &c.Profiles[0]
	if legacy.Gateway != nil {
		p.Gateway = *legacy.Gateway
	}
	if legacy.Port != nil {
		p.Port = *legacy.Port
	}
	if legacy.SAMLPort != nil {
		p.SAMLPort = *legacy.SAMLPort
	}
	if legacy.OpenconnectPath != nil {
		c.OpenconnectPath = *legacy.OpenconnectPath
	}
	if legacy.HelperPath != nil {
		c.HelperPath = *legacy.HelperPath
	}
	if legacy.Autostart != nil {
		c.Autostart = *legacy.Autostart
	}
	normalizeProfile(p)
	return c, true, nil
}

// Load returns defaults for a fresh install (no config.json), or the migrated
// contents of dir/config.json. When a real legacy file is upgraded, the v2
// shape is written back once (0600 in a 0700 dir). A fresh install writes
// nothing — the empty-gateway defaults live only in memory.
func Load(dir string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if os.IsNotExist(err) {
		return defaults(), nil
	}
	if err != nil {
		return nil, err
	}
	cfg, upgraded, err := migrate(data)
	if err != nil {
		return nil, err
	}
	if upgraded {
		if err := cfg.Save(dir); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// Save writes the current (schemaVersion 2) shape to dir/config.json.
func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	c.SchemaVersion = schemaVersion
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600)
}

func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "openfortitray"), nil
}
