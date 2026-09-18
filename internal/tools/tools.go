//go:build tools
// +build tools

// This file tracks build tool and library dependencies.
// See https://github.com/golang/go/wiki/Modules#how-can-i-track-tool-dependencies-for-a-module

package tools

import (
	_ "github.com/wailsapp/wails/v2"
	_ "github.com/energye/systray"
)
