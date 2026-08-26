//go:build linux

#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include "glass_linux.h"

void oft_attach_x11_blur(unsigned long window) {
  // A separate connection from GLFW's own: Xlib supports multiple client
  // connections to the same server, and driver.X11WindowContext gives us
  // only the window id, not GLFW's internal Display*.
  Display *display = XOpenDisplay(NULL);
  if (display == NULL) {
    return;
  }

  Atom blurAtom = XInternAtom(display, "_KDE_NET_WM_BLUR_BEHIND_REGION", False);
  if (blurAtom == None) {
    XCloseDisplay(display);
    return;
  }

  // A zero-length CARDINAL array requests blurring the entire window,
  // per KWin's own convention (matches Konsole/Alacritty's blur-behind
  // implementations).
  XChangeProperty(display, (Window)window, blurAtom, XA_CARDINAL, 32,
                   PropModeReplace, NULL, 0);
  XFlush(display);
  XCloseDisplay(display);
}
