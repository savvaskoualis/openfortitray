package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	c, err := Load(t.TempDir()) // no config.json present
	if err != nil {
		t.Fatal(err)
	}
	// No default gateway: it is deployment-specific, so the app must report that
	// it is unset rather than dial an endpoint nobody asked for.
	if c.Gateway != "" {
		t.Fatalf("gateway must default to empty, got %q", c.Gateway)
	}
	if c.Port != 10443 || c.SAMLPort != 8020 {
		t.Fatalf("bad defaults: %+v", c)
	}
	if c.OpenconnectPath != "openconnect" {
		t.Fatalf("default openconnect path should be bare name, got %q", c.OpenconnectPath)
	}
	if !c.Autostart {
		t.Fatal("autostart should default to true")
	}
	// The sudoers rule written by scripts/install.sh is scoped to exactly this
	// path; a mismatch means sudo asks for a password and the tunnel never
	// starts.
	if c.HelperPath != "/usr/local/libexec/postern-tunnel" {
		t.Fatalf("default helper path must match the install location, got %q", c.HelperPath)
	}
}

func TestLoadOverridesHelperPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"helper_path":"/opt/custom/tunnel"}`), 0o600)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.HelperPath != "/opt/custom/tunnel" {
		t.Fatalf("helper_path not honoured: %+v", c)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, _ := Load(dir)
	c.Gateway = "other.example.com"
	c.Autostart = false
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Gateway != "other.example.com" || c2.Autostart {
		t.Fatalf("round trip lost data: %+v", c2)
	}
}

func TestLoadOverlayKeepsUnsetDefaults(t *testing.T) {
	dir := t.TempDir()
	// partial file: only gateway set
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"gateway":"x.example.com"}`), 0o600)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Gateway != "x.example.com" || c.Port != 10443 {
		t.Fatalf("overlay broken: %+v", c)
	}
}

func TestGatewayURL(t *testing.T) {
	c, _ := Load(t.TempDir())
	c.Gateway = "vpn.example.com"
	if got := c.GatewayURL(); got != "https://vpn.example.com:10443" {
		t.Fatalf("got %q", got)
	}
}
