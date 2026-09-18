#include <X11/Xlib.h>
#include "cursor_linux.h"

int oft_cursor_position(int *x, int *y) {
    Display *d = XOpenDisplay(NULL);
    if (d == NULL) {
        // No X server reachable -- the expected outcome on a pure-Wayland
        // session with no XWayland compatibility layer running. Wayland's
        // security model deliberately forbids an arbitrary client from
        // querying the global cursor position at all, so there is no
        // equivalent native API to fall back to on that session type; the
        // Go caller falls back to fixed-corner placement instead.
        return 0;
    }

    Window root = DefaultRootWindow(d);
    Window returned_root, returned_child;
    int root_x, root_y, win_x, win_y;
    unsigned int mask;

    int ok = XQueryPointer(d, root, &returned_root, &returned_child,
                            &root_x, &root_y, &win_x, &win_y, &mask);
    XCloseDisplay(d);
    if (!ok) {
        return 0;
    }

    *x = root_x;
    *y = root_y;
    return 1;
}
