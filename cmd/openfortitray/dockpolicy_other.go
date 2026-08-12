//go:build !darwin

package main

// setAccessoryActivationPolicy is a no-op on non-darwin platforms; the Dock is
// a macOS concept. It exists so the OnStarted call site in main.go compiles
// everywhere.
func setAccessoryActivationPolicy() {}
