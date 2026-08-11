package settings

import (
	"reflect"
	"testing"

	"github.com/savvaskoualis/openfortitray/internal/config"
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
		{"dotted and hyphenated", "sec-hub.hyperio.cloud", false},
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
	for _, m := range []config.ServerCertMode{config.CertWarn, config.CertTrust, config.CertPin} {
		if got := certMode(certModeLabel(m)); got != m {
			t.Errorf("certMode(certModeLabel(%q)) = %q, want round-trip", m, got)
		}
	}
	if got := certModeLabel(config.ServerCertMode("bogus")); got != certWarnLabel {
		t.Errorf("unknown mode should fall back to the warn label, got %q", got)
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
				Auth: config.AuthConfig{Method: config.AuthSAML}, DTLS: true,
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
