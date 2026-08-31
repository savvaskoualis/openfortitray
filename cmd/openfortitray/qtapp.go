package main

import qt "github.com/mappu/miqt/qt6"

// newQApplication constructs the one QApplication instance for the process.
// os.Args is passed through so Qt can strip its own recognized flags
// (-style, -platform, etc.) before the rest of main() sees argv.
func newQApplication(args []string) *qt.QApplication {
	return qt.NewQApplication(args)
}

// execQApplication runs the Qt event loop until quit; returns Qt's exit code.
func execQApplication() int {
	return qt.QApplication_Exec()
}
