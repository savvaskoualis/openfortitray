package settings

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/credstore"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

func TestValidateHost(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		wantErr bool
	}{
		{"empty is allowed (unconfigured profile is savable)", "", false},
		{"a bare host is fine", "vpn.example.com", false},
		{"dotted and hyphenated", "vpn-gw.example.com", false},
		{"a scheme is rejected", "https://vpn.example.com", true},
		{"a port is rejected", "vpn.example.com:10443", true},
		{"a path is rejected", "vpn.example.com/vpn", true},
		{"whitespace is rejected", "vpn example.com", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHost(tc.host)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateHost(%q) err=%v, wantErr=%v", tc.host, err, tc.wantErr)
			}
		})
	}
}

func TestValidatePort(t *testing.T) {
	tests := []struct {
		name    string
		port    string
		wantErr bool
	}{
		{"the default is fine", "10443", false},
		{"low bound", "1", false},
		{"high bound", "65535", false},
		{"zero is out of range", "0", true},
		{"over 65535 is out of range", "65536", true},
		{"negative is out of range", "-1", true},
		{"non-numeric is rejected", "abc", true},
		{"empty is rejected", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePortString(tc.port)
			if (err != nil) != tc.wantErr {
				t.Errorf("validatePortString(%q) err=%v, wantErr=%v", tc.port, err, tc.wantErr)
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	profiles := []config.Profile{{Name: "Work"}, {Name: "Home"}, {Name: "Lab"}}
	tests := []struct {
		name    string
		input   string
		self    int
		wantErr bool
	}{
		{"empty is rejected", "", 0, true},
		{"whitespace-only is rejected", "   ", 0, true},
		{"a fresh name is fine", "Travel", 0, false},
		{"keeping your own name is fine (self excluded)", "Work", 0, false},
		{"colliding with another profile is rejected", "Home", 0, true},
		{"renaming to a sibling's name is rejected", "Lab", 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateName(tc.input, profiles, tc.self)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateName(%q, self=%d) err=%v, wantErr=%v", tc.input, tc.self, err, tc.wantErr)
			}
		})
	}
}

// Renaming a profile must keep ActiveProfile pointing at it when it was the
// active one; otherwise the active pointer is orphaned and Active() falls back
// to the first profile — dialing the wrong VPN after Save.
func TestRenameProfile(t *testing.T) {
	tests := []struct {
		name       string
		active     string
		sel        int
		newName    string
		wantActive string
	}{
		{
			name:       "renaming the active (non-first) profile moves ActiveProfile with it",
			active:     "Home",
			sel:        1,
			newName:    "Home VPN",
			wantActive: "Home VPN",
		},
		{
			name:       "renaming a non-active profile leaves ActiveProfile alone",
			active:     "Work",
			sel:        1,
			newName:    "Home VPN",
			wantActive: "Work",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &config.Config{
				ActiveProfile: tc.active,
				Profiles: []config.Profile{
					{Name: "Work", Gateway: "work.example.com"},
					{Name: "Home", Gateway: "home.example.com"},
				},
			}
			renameProfile(c, tc.sel, tc.newName)
			if c.ActiveProfile != tc.wantActive {
				t.Errorf("ActiveProfile = %q, want %q", c.ActiveProfile, tc.wantActive)
			}
			if c.Profiles[tc.sel].Name != tc.newName {
				t.Errorf("profile %d name = %q, want %q", tc.sel, c.Profiles[tc.sel].Name, tc.newName)
			}
			// The active pointer must resolve to a real, non-fallback profile: the
			// name Active() returns must equal ActiveProfile (no silent Profiles[0]).
			if got := c.Active(); got.Name != c.ActiveProfile {
				t.Errorf("Active() = %q, want it to resolve to ActiveProfile %q (orphaned pointer)",
					got.Name, c.ActiveProfile)
			}
		})
	}
}

func TestCanDeleteProfile(t *testing.T) {
	if canDeleteProfile(1) {
		t.Error("must refuse deleting the last remaining profile")
	}
	if !canDeleteProfile(2) {
		t.Error("must allow deleting when more than one profile exists")
	}
}

func TestEffectivePort(t *testing.T) {
	if got := effectivePort(false, 9999); got != defaultPort {
		t.Errorf("custom-port off should force %d, got %d", defaultPort, got)
	}
	if got := effectivePort(true, 9999); got != 9999 {
		t.Errorf("custom-port on should keep the entered port, got %d", got)
	}
}

func TestNormalizePorts(t *testing.T) {
	c := &config.Config{Profiles: []config.Profile{
		{Name: "custom on keeps its port", CustomPort: true, Port: 8443},
		{Name: "custom off is reset", CustomPort: false, Port: 12345},
	}}
	normalizePorts(c)
	if c.Profiles[0].Port != 8443 {
		t.Errorf("custom-on port = %d, want 8443", c.Profiles[0].Port)
	}
	if c.Profiles[1].Port != defaultPort {
		t.Errorf("custom-off port = %d, want %d", c.Profiles[1].Port, defaultPort)
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		wantErr bool
	}{
		{
			name:    "no profiles is rejected",
			cfg:     &config.Config{Profiles: nil},
			wantErr: true,
		},
		{
			name: "a valid single profile passes (empty gateway allowed)",
			cfg: &config.Config{Profiles: []config.Profile{
				{Name: "Default", Gateway: "", Port: 10443},
			}},
			wantErr: false,
		},
		{
			name: "duplicate names are rejected",
			cfg: &config.Config{Profiles: []config.Profile{
				{Name: "Dup", Port: 10443}, {Name: "Dup", Port: 10443},
			}},
			wantErr: true,
		},
		{
			name: "an empty name is rejected",
			cfg: &config.Config{Profiles: []config.Profile{
				{Name: "", Port: 10443},
			}},
			wantErr: true,
		},
		{
			name: "a bad custom port is rejected",
			cfg: &config.Config{Profiles: []config.Profile{
				{Name: "Work", Gateway: "vpn.example.com", CustomPort: true, Port: 0},
			}},
			wantErr: true,
		},
		{
			name: "a bad port is ignored when custom-port is off",
			cfg: &config.Config{Profiles: []config.Profile{
				{Name: "Work", Gateway: "vpn.example.com", CustomPort: false, Port: 0},
			}},
			wantErr: false,
		},
		{
			name: "a scheme in the gateway is rejected",
			cfg: &config.Config{Profiles: []config.Profile{
				{Name: "Work", Gateway: "https://vpn.example.com", Port: 10443},
			}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateConfig err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateFingerprint(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty is rejected (required when pinning)", "", true},
		{"bare hex ok", "abcdef0123456789", false},
		{"colon-separated hex ok", "AB:CD:EF:01", false},
		{"sha256 prefix has a colon, still charset-valid", "sha256:AB:CD", true}, // 's','h','a' are not hex
		{"uppercase hex ok", "ABCDEF", false},
		{"stray character rejected", "AB:CD:GZ", true},
		{"spaces rejected", "AB CD", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateFingerprint(tc.in); (err != nil) != tc.wantErr {
				t.Errorf("validateFingerprint(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			}
		})
	}
	// The live entry validator tolerates empty (so a non-pin mode never wedges
	// the form) but still enforces the charset.
	if err := fingerprintCharset(""); err != nil {
		t.Errorf("fingerprintCharset(\"\") should be nil, got %v", err)
	}
	if err := fingerprintCharset("nothex"); err == nil {
		t.Error("fingerprintCharset should reject non-hex")
	}
}

func TestValidateDomainAndSplitDNS(t *testing.T) {
	domainTests := []struct {
		in      string
		wantErr bool
	}{
		{"corp.example.com", false},
		{"internal", false}, // single label allowed for split-DNS suffixes
		{"a-b.example.io", false},
		{"", true},
		{"-bad.example.com", true}, // label starts with hyphen
		{"bad-.example.com", true}, // label ends with hyphen
		{"has space.com", true},
		{"https://x.com", true},
		{"x..y", true}, // empty label
	}
	for _, tc := range domainTests {
		t.Run("domain/"+tc.in, func(t *testing.T) {
			if err := validateDomain(tc.in); (err != nil) != tc.wantErr {
				t.Errorf("validateDomain(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			}
		})
	}

	// The multi-line field: blanks are skipped, empty is allowed, one bad line
	// fails the whole field.
	if err := validateSplitDNSText(""); err != nil {
		t.Errorf("empty split-DNS should be valid, got %v", err)
	}
	if err := validateSplitDNSText("corp.example.com\n\n  internal  \n"); err != nil {
		t.Errorf("valid split-DNS with blanks/whitespace should pass, got %v", err)
	}
	if err := validateSplitDNSText("corp.example.com\nbad domain"); err == nil {
		t.Error("a bad line should fail the whole split-DNS field")
	}
	got := parseSplitDNS("corp.example.com\n\n  internal  \n")
	if !reflect.DeepEqual(got, []string{"corp.example.com", "internal"}) {
		t.Errorf("parseSplitDNS = %q, want [corp.example.com internal]", got)
	}
	if parseSplitDNS("\n  \n") != nil {
		t.Error("an all-blank field should parse to nil")
	}
}

func TestCertModeRoundTrip(t *testing.T) {
	// Only the two offered modes round-trip; "Trust (accept invalid)" was removed.
	for _, m := range []config.ServerCertMode{config.CertWarn, config.CertPin} {
		if got := certMode(certModeLabel(m)); got != m {
			t.Errorf("certMode(certModeLabel(%q)) = %q, want round-trip", m, got)
		}
	}
	// The retired trust mode and any unknown mode fall back to the warn label, so
	// a legacy config that stored "trust" is displayed (and re-saved) as warn.
	if got := certModeLabel(config.CertTrust); got != certWarnLabel {
		t.Errorf("retired trust mode should map to the warn label, got %q", got)
	}
	if got := certModeLabel(config.ServerCertMode("bogus")); got != certWarnLabel {
		t.Errorf("unknown mode should fall back to the warn label, got %q", got)
	}
	// The RadioGroup must no longer offer a Trust option.
	for _, l := range certModeLabels {
		if l == "Trust (accept invalid)" {
			t.Error("the Trust (accept invalid) option must be removed from the cert-mode radio")
		}
	}
}

// FirstConnectIssue must point Connect's caller at the first blocking problem in
// the active profile — the right tab, the right field, a message that names the
// fix — or return nil when the profile is ready to dial.
func TestFirstConnectIssue(t *testing.T) {
	tests := []struct {
		name       string
		profile    config.Profile
		wantNil    bool
		wantTab    string
		wantField  string
		wantMsgSub string
	}{
		{
			name:    "a ready SAML profile has no issue",
			profile: config.Profile{Name: "Work", Gateway: "vpn.example.com", Port: 10443, Auth: config.AuthConfig{Method: config.AuthSAML}},
			wantNil: true,
		},
		{
			name:    "empty gateway routes to Basic gateway",
			profile: config.Profile{Name: "Work", Gateway: ""},
			wantTab: TabBasic, wantField: FieldGateway, wantMsgSub: "gateway host",
		},
		{
			name:    "malformed gateway routes to Basic gateway",
			profile: config.Profile{Name: "Work", Gateway: "https://vpn.example.com"},
			wantTab: TabBasic, wantField: FieldGateway, wantMsgSub: "bare host",
		},
		{
			name:    "out-of-range custom port routes to Basic port",
			profile: config.Profile{Name: "Work", Gateway: "vpn.example.com", CustomPort: true, Port: 70000},
			wantTab: TabBasic, wantField: FieldPort, wantMsgSub: "port",
		},
		{
			// credstore is seeded with a PSK for this exact gateway below, before
			// the loop runs.
			name:    "a ready ipsec profile with psk auth has no issue",
			profile: config.Profile{Name: "Work", Gateway: "vpn-psk-stored.example.com", Backend: config.BackendIPsec, Auth: config.AuthConfig{Method: config.AuthSAML}, IPsec: config.IPsecConfig{AuthMethod: config.IPsecAuthPSK}},
			wantNil: true,
		},
		{
			name:    "ipsec psk profile with no stored secret routes to Basic pre-shared key",
			profile: config.Profile{Name: "Work", Gateway: "vpn-psk-missing.example.com", Backend: config.BackendIPsec, Auth: config.AuthConfig{Method: config.AuthSAML}, IPsec: config.IPsecConfig{AuthMethod: config.IPsecAuthPSK}},
			wantTab: TabBasic, wantField: FieldIPsecSecret, wantMsgSub: "pre-shared key",
		},
		{
			name:    "ipsec cert auth without a certificate routes to Basic cert path",
			profile: config.Profile{Name: "Work", Gateway: "vpn.example.com", Backend: config.BackendIPsec, Auth: config.AuthConfig{Method: config.AuthSAML}, IPsec: config.IPsecConfig{AuthMethod: config.IPsecAuthCert}},
			wantTab: TabBasic, wantField: FieldIPsecCertPath, wantMsgSub: "certificate",
		},
		{
			name:    "ipsec cert auth with a certificate but no key routes to Basic key path",
			profile: config.Profile{Name: "Work", Gateway: "vpn.example.com", Backend: config.BackendIPsec, Auth: config.AuthConfig{Method: config.AuthSAML}, IPsec: config.IPsecConfig{AuthMethod: config.IPsecAuthCert, CertPath: "/x.crt"}},
			wantTab: TabBasic, wantField: FieldIPsecKeyPath, wantMsgSub: "private key",
		},
		{
			name:    "unsupported password auth routes to Basic auth",
			profile: config.Profile{Name: "Work", Gateway: "vpn.example.com", Auth: config.AuthConfig{Method: config.AuthPassword}},
			wantTab: TabBasic, wantField: FieldAuth, wantMsgSub: "SAML",
		},
		{
			name:    "pin mode without a fingerprint routes to Advanced server certificate",
			profile: config.Profile{Name: "Work", Gateway: "vpn.example.com", Auth: config.AuthConfig{Method: config.AuthSAML}, ServerCert: config.ServerCert{Mode: config.CertPin}},
			wantTab: TabAdvanced, wantField: FieldServerCert, wantMsgSub: "fingerprint",
		},
		{
			name:    "invalid split-DNS domain routes to Advanced split-DNS",
			profile: config.Profile{Name: "Work", Gateway: "vpn.example.com", Auth: config.AuthConfig{Method: config.AuthSAML}, SplitDNS: []string{"bad domain"}},
			wantTab: TabAdvanced, wantField: FieldSplitDNS, wantMsgSub: "split-DNS",
		},
	}

	// validateIPsecPSKPresent reads credstore; swap in an in-memory fake so this
	// test never touches the real OS keychain, and seed the one gateway the
	// "ready" ipsec/psk case above expects to already have a secret.
	restore := credstore.SetBackend(credstore.NewMemory())
	defer restore()
	if err := credstore.Set(config.IPsecPSKCredstoreKey("vpn-psk-stored.example.com"), "s3cr3t"); err != nil {
		t.Fatalf("seeding credstore: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{ActiveProfile: tc.profile.Name, Profiles: []config.Profile{tc.profile}}
			got := FirstConnectIssue(cfg)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want no issue, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("want an issue, got nil")
			}
			if got.ProfileName != tc.profile.Name {
				t.Errorf("ProfileName = %q, want %q", got.ProfileName, tc.profile.Name)
			}
			if got.Tab != tc.wantTab {
				t.Errorf("Tab = %q, want %q", got.Tab, tc.wantTab)
			}
			if got.Field != tc.wantField {
				t.Errorf("Field = %q, want %q", got.Field, tc.wantField)
			}
			if !strings.Contains(got.Message, tc.wantMsgSub) {
				t.Errorf("Message = %q, want it to contain %q", got.Message, tc.wantMsgSub)
			}
		})
	}
}

// A profile broken in several ways must surface only the most fundamental fix
// first (gateway → port → auth → Advanced), and reveal the next as each is
// fixed. First-issue-wins is what makes the reconnect-after-fix loop terminate.
func TestFirstConnectIssueOrderingFirstWins(t *testing.T) {
	p := config.Profile{
		Name:       "Work",
		Gateway:    "",                                             // gateway issue (most fundamental)
		Auth:       config.AuthConfig{Method: config.AuthPassword}, // and an auth issue
		ServerCert: config.ServerCert{Mode: config.CertPin},        // and a server-cert issue
		SplitDNS:   []string{"bad domain"},                         // and a split-DNS issue
	}
	cfg := &config.Config{ActiveProfile: "Work", Profiles: []config.Profile{p}}

	if got := FirstConnectIssue(cfg); got == nil || got.Field != FieldGateway {
		t.Fatalf("want the gateway issue first, got %+v", got)
	}
	cfg.Profiles[0].Gateway = "vpn.example.com"
	if got := FirstConnectIssue(cfg); got == nil || got.Field != FieldAuth {
		t.Fatalf("want the auth issue next, got %+v", got)
	}
	cfg.Profiles[0].Auth.Method = config.AuthSAML
	if got := FirstConnectIssue(cfg); got == nil || got.Field != FieldServerCert {
		t.Fatalf("want the server-certificate issue next, got %+v", got)
	}
	cfg.Profiles[0].ServerCert = config.ServerCert{Mode: config.CertWarn}
	if got := FirstConnectIssue(cfg); got == nil || got.Field != FieldSplitDNS {
		t.Fatalf("want the split-DNS issue last, got %+v", got)
	}
	cfg.Profiles[0].SplitDNS = nil
	if got := FirstConnectIssue(cfg); got != nil {
		t.Fatalf("want no issue once everything is fixed, got %+v", got)
	}
}

// Save must refuse to activate a profile whose auth method has no runtime, and
// must accept a SAML active profile even if a non-active one is non-SAML.
func TestValidateAuthGating(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		wantErr bool
	}{
		{
			name: "active SAML profile passes",
			cfg: &config.Config{
				ActiveProfile: "Work", OpenconnectPath: "openconnect",
				Profiles: []config.Profile{{Name: "Work", Auth: config.AuthConfig{Method: config.AuthSAML}}},
			},
			wantErr: false,
		},
		{
			name: "active password profile is rejected",
			cfg: &config.Config{
				ActiveProfile: "Work", OpenconnectPath: "openconnect",
				Profiles: []config.Profile{{Name: "Work", Auth: config.AuthConfig{Method: config.AuthPassword}}},
			},
			wantErr: true,
		},
		{
			name: "active cert profile is rejected",
			cfg: &config.Config{
				ActiveProfile: "Work", OpenconnectPath: "openconnect",
				Profiles: []config.Profile{{Name: "Work", Auth: config.AuthConfig{Method: config.AuthCert}}},
			},
			wantErr: true,
		},
		{
			name: "a non-active non-SAML profile does not block Save",
			cfg: &config.Config{
				ActiveProfile: "Work", OpenconnectPath: "openconnect",
				Profiles: []config.Profile{
					{Name: "Work", Auth: config.AuthConfig{Method: config.AuthSAML}},
					{Name: "Lab", Auth: config.AuthConfig{Method: config.AuthPassword}},
				},
			},
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateConfig(tc.cfg); (err != nil) != tc.wantErr {
				t.Errorf("validateConfig err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// Save no longer refuses an IPsec profile at all — the strongSwan/native
// Windows runtimes exist now, and validateBackendSupported (the old refusal)
// is gone. An IPsec profile — even one incomplete for its chosen auth method
// — still saves; FirstConnectIssue is what gates Connect on completeness.
func TestValidateConfigAcceptsIPsecBackend(t *testing.T) {
	cfg := &config.Config{
		ActiveProfile: "Work", OpenconnectPath: "openconnect",
		Profiles: []config.Profile{{Name: "Work", Backend: config.BackendIPsec, Auth: config.AuthConfig{Method: config.AuthSAML}}},
	}
	if err := validateConfig(cfg); err != nil {
		t.Errorf("validateConfig should accept an IPsec profile now, got err=%v", err)
	}
}

// The Pin server-cert mode requires a valid fingerprint; the other modes ignore
// the pin field entirely.
func TestValidateConfigServerCertPin(t *testing.T) {
	tests := []struct {
		name    string
		profile config.Profile
		wantErr bool
	}{
		{
			name:    "pin mode with a valid fingerprint passes",
			profile: config.Profile{Name: "P", ServerCert: config.ServerCert{Mode: config.CertPin, Pin: "AB:CD:01"}},
			wantErr: false,
		},
		{
			name:    "pin mode with an empty fingerprint is rejected",
			profile: config.Profile{Name: "P", ServerCert: config.ServerCert{Mode: config.CertPin, Pin: ""}},
			wantErr: true,
		},
		{
			name:    "pin mode with a bad fingerprint is rejected",
			profile: config.Profile{Name: "P", ServerCert: config.ServerCert{Mode: config.CertPin, Pin: "not-hex"}},
			wantErr: true,
		},
		{
			name:    "trust mode ignores the (absent) fingerprint",
			profile: config.Profile{Name: "P", ServerCert: config.ServerCert{Mode: config.CertTrust}},
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{ActiveProfile: "P", OpenconnectPath: "openconnect", Profiles: []config.Profile{tc.profile}}
			if err := validateConfig(cfg); (err != nil) != tc.wantErr {
				t.Errorf("validateConfig err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateOpenconnectPath(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"", false}, // Save tolerates empty; Load supplies the default
		{"openconnect", false},
		{"/usr/local/bin/openconnect", false},
		{"oc-1.2_beta+x", false},
		{"has space", true},
		{"semi;colon", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if err := validateOpenconnectPath(tc.in); (err != nil) != tc.wantErr {
				t.Errorf("validateOpenconnectPath(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			}
		})
	}
	if err := openconnectPathEntryValidator("  "); err == nil {
		t.Error("the entry validator should require a non-blank path")
	}
}

func TestUniqueName(t *testing.T) {
	profiles := []config.Profile{{Name: "Work"}, {Name: "Work copy"}, {Name: "Work copy 2"}}
	if got := uniqueName("Home", profiles); got != "Home" {
		t.Errorf("unused base should be returned as-is, got %q", got)
	}
	if got := uniqueName("Work copy", profiles); got != "Work copy 3" {
		t.Errorf("taken base should get the first free suffix, got %q", got)
	}
}

func TestAuthLabelRoundTrip(t *testing.T) {
	for _, m := range []config.AuthMethod{config.AuthSAML, config.AuthPassword, config.AuthCert} {
		if got := authMethod(authLabel(m)); got != m {
			t.Errorf("authMethod(authLabel(%q)) = %q, want round-trip", m, got)
		}
	}
	if got := authLabel(config.AuthMethod("bogus")); got != authSAMLLabel {
		t.Errorf("unknown method should fall back to SAML label, got %q", got)
	}
}

func TestAuthMethodNoteText(t *testing.T) {
	tests := []struct {
		name   string
		method config.AuthMethod
		want   string
	}{
		{"saml is the only wired method", config.AuthSAML, ""},
		{"password not yet supported", config.AuthPassword,
			"(username/password auth not yet supported — use SAML/SSO)"},
		{"cert not yet supported", config.AuthCert,
			"(client-certificate auth not yet supported — use SAML/SSO)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := authMethodNoteText(tc.method); got != tc.want {
				t.Errorf("authMethodNoteText(%v) = %q, want %q", tc.method, got, tc.want)
			}
		})
	}
}

func TestBackendLabelRoundTrip(t *testing.T) {
	for _, b := range []config.Backend{config.BackendSSL, config.BackendIPsec} {
		if got := backendFromLabel(backendLabel(b)); got != b {
			t.Errorf("backendFromLabel(backendLabel(%q)) = %q, want round-trip", b, got)
		}
	}
	if got := backendLabel(config.Backend("bogus")); got != backendSSLLabel {
		t.Errorf("unknown backend should fall back to SSL label, got %q", got)
	}
	if got := backendFromLabel("bogus label"); got != config.BackendSSL {
		t.Errorf("unknown label should fall back to SSL backend, got %q", got)
	}
}

func TestIPsecAuthLabelRoundTrip(t *testing.T) {
	for _, m := range []config.IPsecAuthMethod{config.IPsecAuthPSK, config.IPsecAuthCert} {
		if got := ipsecAuthFromLabel(ipsecAuthLabel(m)); got != m {
			t.Errorf("round trip: %q -> %q -> %q", m, ipsecAuthLabel(m), got)
		}
	}
}

func TestValidateIPsecFieldsPresentPSKNeedsNothingHere(t *testing.T) {
	field, msg := validateIPsecFieldsPresent(config.IPsecConfig{AuthMethod: config.IPsecAuthPSK})
	if field != "" || msg != "" {
		t.Errorf("PSK: got field=%q msg=%q, want both empty (secret lives in credstore, checked elsewhere)", field, msg)
	}
}

func TestValidateIPsecFieldsPresentCertRequiresCertAndKey(t *testing.T) {
	field, _ := validateIPsecFieldsPresent(config.IPsecConfig{AuthMethod: config.IPsecAuthCert})
	if field != FieldIPsecCertPath {
		t.Errorf("no cert/key set: field = %q, want %q", field, FieldIPsecCertPath)
	}
	field, _ = validateIPsecFieldsPresent(config.IPsecConfig{
		AuthMethod: config.IPsecAuthCert, CertPath: "/x.crt"})
	if field != FieldIPsecKeyPath {
		t.Errorf("cert set, no key: field = %q, want %q", field, FieldIPsecKeyPath)
	}
	field, _ = validateIPsecFieldsPresent(config.IPsecConfig{
		AuthMethod: config.IPsecAuthCert, CertPath: "/x.crt", KeyPath: "/x.key"})
	if field != "" {
		t.Errorf("both set: field = %q, want empty", field)
	}
}

// validateIPsecPSKPresent does real I/O (a credstore read), so it is tested
// against an in-memory fake backend rather than the real OS keychain.
func TestValidateIPsecPSKPresent(t *testing.T) {
	restore := credstore.SetBackend(credstore.NewMemory())
	defer restore()

	if err := validateIPsecPSKPresent("vpn.example.com"); err == nil {
		t.Error("no secret stored: want an error, got nil")
	}
	if err := credstore.Set(config.IPsecPSKCredstoreKey("vpn.example.com"), ""); err != nil {
		t.Fatalf("credstore.Set: %v", err)
	}
	if err := validateIPsecPSKPresent("vpn.example.com"); err == nil {
		t.Error("empty secret stored: want an error, got nil")
	}
	if err := credstore.Set(config.IPsecPSKCredstoreKey("vpn.example.com"), "s3cr3t"); err != nil {
		t.Fatalf("credstore.Set: %v", err)
	}
	if err := validateIPsecPSKPresent("vpn.example.com"); err != nil {
		t.Errorf("secret stored: want nil, got %v", err)
	}
	// A different gateway's PSK must not satisfy this one — keys are
	// namespaced per-gateway.
	if err := validateIPsecPSKPresent("other.example.com"); err == nil {
		t.Error("different gateway, no secret stored for it: want an error, got nil")
	}
}

// busyThenBackend is a credstore.Backend fake that returns credstore.ErrBusy
// for the first busyFor calls to Get, then falls through to an in-memory
// store — simulating the OS secret store racing an autostart-at-login launch
// (e.g. the macOS login keychain mid-unlock) before it settles.
type busyThenBackend struct {
	mem     *credstore.Memory
	busyFor int
	calls   int
}

func newBusyThenBackend(busyFor int) *busyThenBackend {
	return &busyThenBackend{mem: credstore.NewMemory(), busyFor: busyFor}
}

func (b *busyThenBackend) Get(key string) (string, error) {
	b.calls++
	if b.calls <= b.busyFor {
		return "", credstore.ErrBusy
	}
	return b.mem.Get(key)
}
func (b *busyThenBackend) Set(key, value string) error { return b.mem.Set(key, value) }
func (b *busyThenBackend) Delete(key string) error     { return b.mem.Delete(key) }

// permanentlyBusyBackend always reports credstore.ErrBusy, so
// validateIPsecPSKPresent's retry loop must eventually give up rather than
// hang or spin forever.
type permanentlyBusyBackend struct{}

func (permanentlyBusyBackend) Get(string) (string, error) { return "", credstore.ErrBusy }
func (permanentlyBusyBackend) Set(string, string) error   { return nil }
func (permanentlyBusyBackend) Delete(string) error        { return nil }

// credstore.ErrBusy (the OS secret store not yet unlocked) must be retried,
// not immediately collapsed to "no PSK stored" — that would wrongly tell the
// user to add a PSK that is already there (Important #3).
func TestValidateIPsecPSKPresentRetriesOnBusyStoreThenSucceeds(t *testing.T) {
	origInterval, origWindow := pskRetryInterval, pskRetryWindow
	pskRetryInterval = time.Millisecond
	pskRetryWindow = time.Second
	defer func() { pskRetryInterval, pskRetryWindow = origInterval, origWindow }()

	backend := newBusyThenBackend(2)
	restore := credstore.SetBackend(backend)
	defer restore()
	if err := backend.Set(config.IPsecPSKCredstoreKey("vpn.example.com"), "s3cr3t"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := validateIPsecPSKPresent("vpn.example.com"); err != nil {
		t.Errorf("validateIPsecPSKPresent = %v, want nil once the store stops being busy", err)
	}
	if backend.calls < 3 {
		t.Errorf("Get called %d times, want at least 3 (two busy + one success)", backend.calls)
	}
}

// A store that never stops reporting busy must not hang: the retry window
// bounds it, and the caller sees a distinguishable error rather than a false
// "no PSK stored".
func TestValidateIPsecPSKPresentGivesUpOnPermanentlyBusyStore(t *testing.T) {
	origInterval, origWindow := pskRetryInterval, pskRetryWindow
	pskRetryInterval = time.Millisecond
	pskRetryWindow = 20 * time.Millisecond
	defer func() { pskRetryInterval, pskRetryWindow = origInterval, origWindow }()

	restore := credstore.SetBackend(permanentlyBusyBackend{})
	defer restore()

	err := validateIPsecPSKPresent("vpn.example.com")
	if err == nil {
		t.Fatal("want an error once the retry window elapses, got nil")
	}
	if !errors.Is(err, credstore.ErrBusy) {
		t.Errorf("error = %v, want it to still wrap credstore.ErrBusy so callers can tell this apart from a genuine miss", err)
	}
}

// reindexIPsecSecrets keeps ipsecSecretDirty/ipsecSecretValue — keyed by
// profile index — aligned with c.work.Profiles after deleteProfile removes
// one element and every later profile shifts left by one. Exercised directly
// against a bare Controller: it only touches these two maps, so it needs no
// host, window, or built widget tree.
func TestReindexIPsecSecretsAfterDelete(t *testing.T) {
	c := &Controller{
		ipsecSecretDirty: map[int]bool{0: true, 1: true, 2: true},
		ipsecSecretValue: map[int]string{0: "a", 1: "b", 2: "c"},
	}
	// Removing the profile that was at index 1 ("b") must drop its own
	// entry, and shift index 2 ("c") down to 1 so it still lines up with
	// its profile's new position; index 0 ("a") is untouched.
	c.reindexIPsecSecrets(1)

	wantValue := map[int]string{0: "a", 1: "c"}
	if !reflect.DeepEqual(c.ipsecSecretValue, wantValue) {
		t.Errorf("ipsecSecretValue = %v, want %v", c.ipsecSecretValue, wantValue)
	}
	wantDirty := map[int]bool{0: true, 1: true}
	if !reflect.DeepEqual(c.ipsecSecretDirty, wantDirty) {
		t.Errorf("ipsecSecretDirty = %v, want %v", c.ipsecSecretDirty, wantDirty)
	}
}

// Removing index 0 (the first profile) must not leave a stale/duplicated
// entry at 0 — every later index shifts down by one and nothing points past
// the new end of the (now shorter) profile list.
func TestReindexIPsecSecretsAfterDeleteFirst(t *testing.T) {
	c := &Controller{
		ipsecSecretDirty: map[int]bool{0: true, 1: true},
		ipsecSecretValue: map[int]string{0: "a", 1: "b"},
	}
	c.reindexIPsecSecrets(0)

	wantValue := map[int]string{0: "b"}
	if !reflect.DeepEqual(c.ipsecSecretValue, wantValue) {
		t.Errorf("ipsecSecretValue = %v, want %v", c.ipsecSecretValue, wantValue)
	}
	wantDirty := map[int]bool{0: true}
	if !reflect.DeepEqual(c.ipsecSecretDirty, wantDirty) {
		t.Errorf("ipsecSecretDirty = %v, want %v", c.ipsecSecretDirty, wantDirty)
	}
}

// cloneConfig must produce an independent copy: edits to the clone (including
// its per-profile SplitDNS slice) must not reach the original, which is what
// lets the window edit a working copy and only commit on Save.
func TestCloneConfigIsDeep(t *testing.T) {
	orig := &config.Config{
		ActiveProfile: "Work",
		Autostart:     true,
		Profiles: []config.Profile{
			{Name: "Work", Gateway: "vpn.example.com", Port: 10443, SplitDNS: []string{"corp.local"}},
		},
	}
	clone := cloneConfig(orig)
	clone.Profiles[0].Gateway = "other.example.com"
	clone.Profiles[0].SplitDNS[0] = "changed.local"
	clone.Autostart = false
	if orig.Profiles[0].Gateway != "vpn.example.com" {
		t.Error("editing the clone's gateway leaked into the original")
	}
	if orig.Profiles[0].SplitDNS[0] != "corp.local" {
		t.Error("editing the clone's SplitDNS leaked into the original")
	}
	if !orig.Autostart {
		t.Error("editing the clone's Autostart leaked into the original")
	}
}

// The working-copy → Save → Load round-trip: cloning the live config, editing
// the clone and persisting it must reproduce exactly on reload, and must not
// have mutated the original in the process.
func TestWorkingCopySaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	live := &config.Config{
		SchemaVersion: 2,
		ActiveProfile: "Work",
		Autostart:     true,
		Profiles: []config.Profile{
			{Name: "Work", Gateway: "vpn.example.com", Port: 10443, SAMLPort: 8020,
				Auth: config.AuthConfig{Method: config.AuthSAML}, Backend: config.BackendSSL, DTLS: true,
				ServerCert: config.ServerCert{Mode: config.CertWarn}},
		},
		OpenconnectPath: "openconnect",
		HelperPath:      "/usr/local/libexec/openfortitray-tunnel",
	}

	work := cloneConfig(live)
	work.Profiles = append(work.Profiles, config.NewProfile("Lab"))
	work.Profiles[1].Gateway = "lab.example.com"
	work.Profiles[1].CustomPort = true
	work.Profiles[1].Port = 8443
	work.ActiveProfile = "Lab"

	normalizePorts(work)
	if err := validateConfig(work); err != nil {
		t.Fatalf("edited working copy should validate: %v", err)
	}
	if err := work.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := config.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got.Profiles, work.Profiles) {
		t.Errorf("reloaded profiles = %+v, want %+v", got.Profiles, work.Profiles)
	}
	if got.ActiveProfile != "Lab" {
		t.Errorf("reloaded activeProfile = %q, want Lab", got.ActiveProfile)
	}
	// The original live config must be untouched by all of the above.
	if len(live.Profiles) != 1 || live.ActiveProfile != "Work" {
		t.Errorf("editing the working copy mutated the live config: %+v", live)
	}
}

// statusFor must agree with the tray's connect/disconnect enabling: active is
// true exactly when Disconnect (not Connect) should be enabled.
func TestStatusFor(t *testing.T) {
	tests := []struct {
		name       string
		event      tunnel.Event
		wantKind   statusKind
		wantActive bool
		wantText   string
	}{
		{"disconnected", tunnel.Event{State: tunnel.Disconnected}, statusGray, false, "Disconnected"},
		{"connected shows ip", tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.5"}, statusGreen, true, "Connected — 10.0.0.5"},
		{"connected without ip", tunnel.Event{State: tunnel.Connected}, statusGreen, true, "Connected"},
		{"authenticating", tunnel.Event{State: tunnel.Authenticating}, statusYellow, true, "Authenticating…"},
		{"connecting", tunnel.Event{State: tunnel.Connecting}, statusYellow, true, "Connecting…"},
		{"error is terminal and re-enables connect", tunnel.Event{State: tunnel.Error, Detail: "gateway not set"}, statusRed, false, "Error: gateway not set"},
		{"reconnecting keeps only the first line", tunnel.Event{State: tunnel.Reconnecting, Detail: "boom\nmore"}, statusYellow, true, "Reconnecting — boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, kind, active := statusFor(tc.event)
			if kind != tc.wantKind {
				t.Errorf("kind = %v, want %v", kind, tc.wantKind)
			}
			if active != tc.wantActive {
				t.Errorf("active = %v, want %v", active, tc.wantActive)
			}
			if text != tc.wantText {
				t.Errorf("text = %q, want %q", text, tc.wantText)
			}
		})
	}
}
