#ifndef OFT_CURSOR_LINUX_H
#define OFT_CURSOR_LINUX_H

// oft_cursor_position writes the mouse cursor's current X11 root-window
// position, in pixels from the top-left corner, into *x/*y. Returns 1 on
// success, 0 if no X server is reachable (a pure-Wayland session with no
// XWayland, or any other X11 failure).
int oft_cursor_position(int *x, int *y);

// oft_set_window_position moves the top-level window whose _NET_WM_NAME
// (falling back to WM_NAME) matches title to the absolute root-window
// position (x, y) -- the same frame oft_cursor_position reports in -- via
// the EWMH _NET_MOVERESIZE_WINDOW client message, which every modern
// EWMH-compliant window manager (GNOME, KDE, XFCE, ...) honors in
// root-relative absolute coordinates regardless of window-manager
// reparenting (the same mechanism tools like wmctrl use). Returns 1 if a
// matching window was found and the request was sent, 0 if no X server is
// reachable, no window with that title was found, or the window manager
// doesn't publish _NET_CLIENT_LIST/_NET_MOVERESIZE_WINDOW at all (a very
// old or non-EWMH window manager).
int oft_set_window_position(const char *title, int x, int y);

#endif
