#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <string.h>
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

// find_window_by_title searches _NET_CLIENT_LIST (the root window's
// EWMH-published list of top-level managed windows) for one whose title
// matches, preferring _NET_WM_NAME (UTF8, what GTK actually sets) and
// falling back to the older WM_NAME/XFetchName. Returns None (0) if no
// window matches or the window manager doesn't publish _NET_CLIENT_LIST.
static Window find_window_by_title(Display *d, const char *title) {
    Window root = DefaultRootWindow(d);
    Atom clientListAtom = XInternAtom(d, "_NET_CLIENT_LIST", True);
    if (clientListAtom == None) {
        return None;
    }

    Atom actualType;
    int actualFormat;
    unsigned long nItems, bytesAfter;
    unsigned char *data = NULL;
    if (XGetWindowProperty(d, root, clientListAtom, 0, ~0L, False, XA_WINDOW,
                            &actualType, &actualFormat, &nItems, &bytesAfter,
                            &data) != Success || data == NULL) {
        return None;
    }

    Window *windows = (Window *)data;
    Window found = None;
    Atom netWmNameAtom = XInternAtom(d, "_NET_WM_NAME", True);
    Atom utf8Atom = XInternAtom(d, "UTF8_STRING", True);

    for (unsigned long i = 0; i < nItems; i++) {
        char *name = NULL;
        unsigned char *netNameData = NULL;

        if (netWmNameAtom != None && utf8Atom != None) {
            Atom t;
            int f;
            unsigned long n, b;
            if (XGetWindowProperty(d, windows[i], netWmNameAtom, 0, ~0L, False,
                                    utf8Atom, &t, &f, &n, &b,
                                    &netNameData) == Success && netNameData != NULL) {
                name = (char *)netNameData;
            }
        }
        if (name == NULL) {
            // XFetchName allocates its own buffer via XFree-compatible
            // Xmalloc, safe to XFree the same as netNameData below.
            XFetchName(d, windows[i], &name);
        }
        if (name != NULL && strcmp(name, title) == 0) {
            found = windows[i];
        }
        if (name != NULL) {
            XFree(name);
        }
        if (found != None) {
            break;
        }
    }

    XFree(data);
    return found;
}

int oft_set_window_position(const char *title, int x, int y) {
    Display *d = XOpenDisplay(NULL);
    if (d == NULL) {
        return 0;
    }

    Window w = find_window_by_title(d, title);
    if (w == None) {
        XCloseDisplay(d);
        return 0;
    }

    Atom moveResizeAtom = XInternAtom(d, "_NET_MOVERESIZE_WINDOW", False);
    if (moveResizeAtom == None) {
        XCloseDisplay(d);
        return 0;
    }

    XEvent event;
    memset(&event, 0, sizeof(event));
    event.xclient.type = ClientMessage;
    event.xclient.serial = 0;
    event.xclient.send_event = True;
    event.xclient.display = d;
    event.xclient.window = w;
    event.xclient.message_type = moveResizeAtom;
    event.xclient.format = 32;
    // data.l[0]: bits 8-9 are the "x present"/"y present" flags (we are not
    // resizing, so the width/height-present flags, bits 10-11, stay unset);
    // the low byte is the gravity, left 0 to mean "use the window's own
    // gravity" per the EWMH spec, since this window is undecorated and
    // gravity only matters for decoration offsets.
    event.xclient.data.l[0] = (1 << 8) | (1 << 9);
    event.xclient.data.l[1] = x;
    event.xclient.data.l[2] = y;
    event.xclient.data.l[3] = 0;
    event.xclient.data.l[4] = 0;

    Window root = DefaultRootWindow(d);
    Status sent = XSendEvent(d, root, False,
                              SubstructureNotifyMask | SubstructureRedirectMask, &event);
    XFlush(d);
    XCloseDisplay(d);
    // XSendEvent's Status is nonzero on success (matching Xlib's general
    // convention, unlike XQueryPointer's Bool above) -- checked so a rare
    // failure here genuinely falls back to wailsruntime.WindowSetPosition
    // in the Go caller, rather than being reported as a success that never
    // actually moved anything.
    return sent != 0;
}
