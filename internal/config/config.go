// Package config holds static VPN settings and user preferences.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Gateway         string `json:"gateway"`
	Port            int    `json:"port"`
	SAMLPort        int    `json:"saml_port"`
	OpenconnectPath string `json:"openconnect_path"`
	Autostart       bool   `json:"autostart"`
}

func defaults() *Config {
	return &Config{
		Gateway:         "securityhub.hyperio.cloud",
		Port:            10443,
		SAMLPort:        8020,
		OpenconnectPath: "openconnect",
		Autostart:       true,
	}
}

// Load returns defaults overlaid with dir/config.json when the file exists.
func Load(dir string) (*Config, error) {
	c := defaults()
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse config.json: %w", err)
	}
	return c, nil
}

func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
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
	return filepath.Join(base, "hyp-vpn"), nil
}

func (c *Config) GatewayURL() string {
	return fmt.Sprintf("https://%s:%d", c.Gateway, c.Port)
}
