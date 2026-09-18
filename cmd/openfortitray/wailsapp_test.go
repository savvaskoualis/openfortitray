package main

import (
	"embed"
	"testing"
)

//go:embed testdata_wailsapp/dummy.txt
var testAssets embed.FS

func TestBuildAppOptionsFrameless380x600(t *testing.T) {
	a := &app{}
	opts := buildAppOptions(a, testAssets)

	if !opts.Frameless {
		t.Error("expected Frameless: true")
	}
	if opts.Width != 380 || opts.Height != 600 {
		t.Errorf("expected 380x600, got %dx%d", opts.Width, opts.Height)
	}
	if opts.Title != "OpenFortiTray" {
		t.Errorf("expected title OpenFortiTray, got %q", opts.Title)
	}
	if len(opts.Bind) != 1 {
		t.Fatalf("expected exactly one bound object, got %d", len(opts.Bind))
	}
	if _, ok := opts.Bind[0].(*Bridge); !ok {
		t.Errorf("expected bound object to be *Bridge, got %T", opts.Bind[0])
	}
}
