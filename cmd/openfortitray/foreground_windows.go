package main

import "github.com/savvaskoualis/openfortitray/internal/tray"

// foregroundFunc enables watchFocusLoss on Windows; see app.foreground.
func foregroundFunc() func() (uintptr, bool, bool) { return tray.ForegroundWindow }
