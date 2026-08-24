//go:build !darwin

package main

// setDockActivationPolicy is a no-op off macOS: the Dock is a macOS concept, and
// on Windows and Linux a window-owning process already appears in the taskbar
// without asking for it.
func setDockActivationPolicy() {}

// watchDockActivation is a no-op off macOS. Windows and Linux deliver a taskbar
// click straight to the window, so there is no activation event to translate.
func watchDockActivation(fn func()) {}
