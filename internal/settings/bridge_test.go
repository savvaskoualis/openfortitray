package settings

import (
	"testing"

	"github.com/savvaskoualis/openfortitray/internal/config"
)

func TestValidateReturnsIssueForBadPort(t *testing.T) {
	cfg := &config.Config{
		Profiles: []config.Profile{{Name: "Work", Gateway: "vpn.example.com", CustomPort: true, Port: 0}},
	}
	issue := Validate(cfg)
	if issue == nil {
		t.Fatal("expected a validation issue for port 0, got nil")
	}
}

func TestValidateReturnsNilForGoodConfig(t *testing.T) {
	cfg := &config.Config{
		Profiles: []config.Profile{{Name: "Work", Gateway: "vpn.example.com", CustomPort: true, Port: 443}},
	}
	if issue := Validate(cfg); issue != nil {
		t.Fatalf("expected no issue, got %+v", issue)
	}
}
