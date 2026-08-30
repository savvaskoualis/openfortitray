package main

import qt "github.com/mappu/miqt/qt6"

// attachGlass attaches native blur/acrylic behind w. Must be called after
// w's underlying native window exists (i.e. after Show()) — WinId() can
// return an invalid handle before then. Safe to call repeatedly
// (idempotent per platform — see glass_darwin.m's identifier-tag guard).
func attachGlass(w *qt.QWidget) {
	attachNativeGlass(uintptr(w.WinId()))
}
