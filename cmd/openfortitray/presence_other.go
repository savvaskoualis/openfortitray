//go:build !darwin

package main

// userPresentFunc returns nil — "always present" — on platforms with no
// display-asleep/screen-locked detection wired yet; see waitForUser.
func userPresentFunc() func() bool { return nil }
