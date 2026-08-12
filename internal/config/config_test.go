package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMigrate covers the pure migrate() function: legacy flat -> v2, an
// already-v2 no-op, and malformed JSON. It asserts field placement (gateway,
// port, saml_port land in the Default profile; openconnect_path, helper_path
// and autostart stay top-level) and that an empty gateway is preserved so the
// empty-gateway guard still trips.
func TestMigrate(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantErr      bool
		wantUpgraded bool
		check        func(t *testing.T, c *Config)
	}{
		{
			name:         "legacy flat upgrades to v2",
			raw:          `{"gateway":"vpn.example.com","port":11443,"saml_port":9020,"openconnect_path":"/opt/oc","helper_path":"/opt/helper","autostart":false}`,
			wantUpgraded: true,
			check: func(t *testing.T, c *Config) {
				if c.SchemaVersion != 3 {
					t.Errorf("schemaVersion = %d, want 3 (legacy upgrades to current)", c.SchemaVersion)
				}
				// A legacy file predates remember_session; it must default to true.
				if !c.Profiles[0].RememberSession {
					t.Errorf("remember_session must default true on legacy upgrade")
				}
				if c.ActiveProfile != "Default" {
					t.Errorf("activeProfile = %q, want Default", c.ActiveProfile)
				}
				if len(c.Profiles) != 1 || c.Profiles[0].Name != "Default" {
					t.Fatalf("want one Default profile, got %+v", c.Profiles)
				}
				p := c.Profiles[0]
				if p.Gateway != "vpn.example.com" || p.Port != 11443 || p.SAMLPort != 9020 {
					t.Errorf("per-connection keys did not land in profile: %+v", p)
				}
				// Profile defaults still apply to keys the legacy file lacked.
				if p.Auth.Method != AuthSAML || !p.DTLS || p.ServerCert.Mode != CertWarn {
					t.Errorf("profile defaults not applied: %+v", p)
				}
				if c.OpenconnectPath != "/opt/oc" || c.HelperPath != "/opt/helper" || c.Autostart {
					t.Errorf("top-level keys not preserved: %+v", c)
				}
			},
		},
		{
			name:         "legacy empty gateway stays empty",
			raw:          `{"gateway":"","autostart":true}`,
			wantUpgraded: true,
			check: func(t *testing.T, c *Config) {
				if c.Active().Gateway != "" {
					t.Errorf("empty gateway must be preserved, got %q", c.Active().Gateway)
				}
				// Untouched numeric keys keep their profile defaults.
				if c.Active().Port != 10443 || c.Active().SAMLPort != 8020 {
					t.Errorf("defaults lost: %+v", c.Active())
				}
			},
		},
		{
			name:         "legacy partial keeps top-level default",
			raw:          `{"helper_path":"/opt/custom/tunnel"}`,
			wantUpgraded: true,
			check: func(t *testing.T, c *Config) {
				if c.HelperPath != "/opt/custom/tunnel" {
					t.Errorf("helper_path override lost: %q", c.HelperPath)
				}
				if c.OpenconnectPath != "openconnect" || !c.Autostart {
					t.Errorf("unset top-level keys should keep defaults: %+v", c)
				}
			},
		},
		{
			name:         "already v2 is a no-op passthrough",
			raw:          `{"schemaVersion":2,"activeProfile":"Work","profiles":[{"name":"Work","gateway":"work.example.com","port":10443,"saml_port":8020,"auth":{"method":"saml"},"dtls":true,"server_cert":{"mode":"warn"}}],"openconnect_path":"openconnect","helper_path":"/usr/local/libexec/openfortitray-tunnel","autostart":true}`,
			wantUpgraded: false,
			check: func(t *testing.T, c *Config) {
				// A read of a v2 file keeps its on-disk version in memory (Save later
				// rewrites it as v3), but the missing remember_session backfills true.
				if c.SchemaVersion != 2 || c.ActiveProfile != "Work" {
					t.Errorf("v2 fields not preserved: %+v", c)
				}
				if len(c.Profiles) != 1 || c.Profiles[0].Name != "Work" || c.Profiles[0].Gateway != "work.example.com" {
					t.Errorf("v2 profiles not preserved: %+v", c.Profiles)
				}
				if !c.Profiles[0].RememberSession {
					t.Errorf("v2 file must backfill remember_session=true")
				}
			},
		},
		{
			name:         "v2 backfills defaults for every profile",
			raw:          `{"schemaVersion":2,"activeProfile":"A","profiles":[{"name":"A","gateway":"a","port":11000,"saml_port":9000,"auth":{"method":"password"},"server_cert":{"mode":"pin"}},{"name":"B","gateway":"b"}]}`,
			wantUpgraded: false,
			check: func(t *testing.T, c *Config) {
				if len(c.Profiles) != 2 {
					t.Fatalf("want 2 profiles, got %d", len(c.Profiles))
				}
				// First profile keeps its explicit values.
				if a := c.Profiles[0]; a.Port != 11000 || a.SAMLPort != 9000 || a.Auth.Method != AuthPassword || a.ServerCert.Mode != CertPin {
					t.Errorf("first profile mangled: %+v", a)
				}
				// Second profile omitted port/auth/servercert -> must be backfilled.
				b := c.Profiles[1]
				if b.Port != 10443 || b.SAMLPort != 8020 || b.Auth.Method != AuthSAML || b.ServerCert.Mode != CertWarn {
					t.Errorf("second profile not backfilled: %+v", b)
				}
			},
		},
		{
			name:         "v2 omitted top-level keys keep defaults",
			raw:          `{"schemaVersion":2,"activeProfile":"Only","profiles":[{"name":"Only","gateway":"g"}]}`,
			wantUpgraded: false,
			check: func(t *testing.T, c *Config) {
				if c.OpenconnectPath != "openconnect" || c.HelperPath != "/usr/local/libexec/openfortitray-tunnel" || !c.Autostart {
					t.Errorf("v2 overlay did not keep top-level defaults: %+v", c)
				}
			},
		},
		{
			name:         "v3 honors explicit remember_session false",
			raw:          `{"schemaVersion":3,"activeProfile":"A","profiles":[{"name":"A","gateway":"a","remember_session":false},{"name":"B","gateway":"b","remember_session":true}]}`,
			wantUpgraded: false,
			check: func(t *testing.T, c *Config) {
				if c.SchemaVersion != 3 {
					t.Errorf("schemaVersion = %d, want 3", c.SchemaVersion)
				}
				// A v3 file is trusted as written: no backfill, so an explicit false
				// stays false and an explicit true stays true.
				if c.Profiles[0].RememberSession {
					t.Errorf("explicit remember_session:false must be honored")
				}
				if !c.Profiles[1].RememberSession {
					t.Errorf("explicit remember_session:true must be honored")
				}
			},
		},
		{
			name:    "malformed JSON errors",
			raw:     `{not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, upgraded, err := migrate([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if upgraded != tt.wantUpgraded {
				t.Errorf("upgraded = %v, want %v", upgraded, tt.wantUpgraded)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}

// TestLoadDefaults: a fresh install (no config.json) yields in-memory defaults
// with one empty Default profile and writes no file.
func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.SchemaVersion != 3 || c.ActiveProfile != "Default" {
		t.Fatalf("bad defaults: %+v", c)
	}
	if !c.Active().RememberSession {
		t.Fatal("remember_session should default to true")
	}
	if len(c.Profiles) != 1 {
		t.Fatalf("want one default profile, got %d", len(c.Profiles))
	}
	p := c.Active()
	// No default gateway: it is deployment-specific, so the app must report it
	// is unset rather than dial an endpoint nobody asked for.
	if p.Gateway != "" {
		t.Fatalf("gateway must default to empty, got %q", p.Gateway)
	}
	if p.Port != 10443 || p.SAMLPort != 8020 {
		t.Fatalf("bad profile defaults: %+v", p)
	}
	if p.Auth.Method != AuthSAML || !p.DTLS || p.ServerCert.Mode != CertWarn {
		t.Fatalf("bad profile defaults: %+v", p)
	}
	if c.OpenconnectPath != "openconnect" {
		t.Fatalf("default openconnect path should be bare name, got %q", c.OpenconnectPath)
	}
	if !c.Autostart {
		t.Fatal("autostart should default to true")
	}
	// The sudoers rule written by scripts/install.sh is scoped to exactly this
	// path; a mismatch means sudo asks for a password and the tunnel fails.
	if c.HelperPath != "/usr/local/libexec/openfortitray-tunnel" {
		t.Fatalf("default helper path must match install location, got %q", c.HelperPath)
	}
	// Fresh install must not materialize a file.
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("fresh install must not write config.json, stat err = %v", err)
	}
}

// TestLoadMigratesAndPersists: a real legacy file is upgraded and the v2 shape
// is written back once.
func TestLoadMigratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"gateway":"x.example.com","port":10443}`), 0o600)

	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Active().Gateway != "x.example.com" || c.Active().Port != 10443 {
		t.Fatalf("migration lost data: %+v", c.Active())
	}

	// The upgraded file must now be v2 on disk: reloading is a no-op passthrough.
	c2, upgraded, err := migrate(mustRead(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if upgraded {
		t.Fatal("re-migrating the persisted file should be a no-op")
	}
	if c2.SchemaVersion != 3 || c2.Active().Gateway != "x.example.com" {
		t.Fatalf("persisted file is not v3: %+v", c2)
	}
}

// TestSaveLoadRoundTrip: v2 shape survives a Save/Load cycle.
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, _ := Load(dir)
	c.Active().Gateway = "other.example.com"
	c.Active().Realm = "corp"
	c.Autostart = false
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Active().Gateway != "other.example.com" || c2.Active().Realm != "corp" || c2.Autostart {
		t.Fatalf("round trip lost data: %+v", c2)
	}
	if c2.SchemaVersion != 3 {
		t.Fatalf("round trip must stay at v3, got %d", c2.SchemaVersion)
	}
}

// TestActive covers the three fallbacks: named match, missing name -> first
// profile, and no profiles -> synthesized empty default (Gateway=="").
func TestActive(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		wantName    string
		wantGateway string
	}{
		{
			name: "named profile",
			cfg: Config{
				ActiveProfile: "B",
				Profiles: []Profile{
					{Name: "A", Gateway: "a"},
					{Name: "B", Gateway: "b"},
				},
			},
			wantName:    "B",
			wantGateway: "b",
		},
		{
			name: "missing name falls to first",
			cfg: Config{
				ActiveProfile: "nope",
				Profiles: []Profile{
					{Name: "A", Gateway: "a"},
					{Name: "B", Gateway: "b"},
				},
			},
			wantName:    "A",
			wantGateway: "a",
		},
		{
			name:        "no profiles synthesizes empty default",
			cfg:         Config{ActiveProfile: "x"},
			wantName:    "Default",
			wantGateway: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.Active()
			if got.Name != tt.wantName {
				t.Errorf("name = %q, want %q", got.Name, tt.wantName)
			}
			if got.Gateway != tt.wantGateway {
				t.Errorf("gateway = %q, want %q", got.Gateway, tt.wantGateway)
			}
		})
	}
}

func TestGatewayURL(t *testing.T) {
	p := &Profile{Gateway: "vpn.example.com", Port: 10443}
	if got := p.GatewayURL(); got != "https://vpn.example.com:10443" {
		t.Fatalf("got %q", got)
	}
}

func mustRead(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
