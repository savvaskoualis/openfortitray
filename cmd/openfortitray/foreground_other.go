//go:build !windows

package main

// foregroundFunc returns nil: outside Windows the frontend's blur event hides
// the window directly (see app.hidesOnBlur).
func foregroundFunc() func() (uintptr, bool, bool) { return nil }
