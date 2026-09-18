#ifndef OFT_CURSOR_LINUX_H
#define OFT_CURSOR_LINUX_H

// oft_cursor_position writes the mouse cursor's current X11 root-window
// position, in pixels from the top-left corner, into *x/*y. Returns 1 on
// success, 0 if no X server is reachable (a pure-Wayland session with no
// XWayland, or any other X11 failure).
int oft_cursor_position(int *x, int *y);

#endif
