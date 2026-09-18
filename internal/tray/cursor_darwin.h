#ifndef OFT_CURSOR_DARWIN_H
#define OFT_CURSOR_DARWIN_H

// oft_cursor_position writes the mouse cursor's current position, in pixels
// from the top-left corner of the primary (menu-bar) screen, into *x/*y.
// Returns 1 on success, 0 if AppKit reports zero screens (should not happen
// on a real desktop session).
int oft_cursor_position(double *x, double *y);

// oft_set_window_position moves the NSWindow whose title matches `title`
// (NUL-terminated UTF-8) so its TOP-LEFT corner sits at (topLeftX,
// topLeftY) -- pixels from the top-left of the primary screen, the same
// frame oft_cursor_position reports in. This sets the window's frame
// origin directly in AppKit's own absolute coordinate space, deliberately
// bypassing Wails' own WindowSetPosition -- which is relative to whichever
// screen the window CURRENTLY occupies (NSWindow.screen), not the primary
// screen, so it silently mis-targets a cursor-anchored popover on a real
// multi-monitor Mac once the window's current screen differs from primary.
// Returns 1 if a window with that title was found and moved, 0 otherwise
// (no such window, or AppKit reports zero screens).
int oft_set_window_position(const char *title, double topLeftX, double topLeftY);

#endif
