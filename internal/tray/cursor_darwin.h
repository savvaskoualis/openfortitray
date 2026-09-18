#ifndef OFT_CURSOR_DARWIN_H
#define OFT_CURSOR_DARWIN_H

// oft_cursor_position writes the mouse cursor's current position, in pixels
// from the top-left corner of the primary (menu-bar) screen, into *x/*y.
// Returns 1 on success, 0 if AppKit reports zero screens (should not happen
// on a real desktop session).
int oft_cursor_position(double *x, double *y);

#endif
