#ifndef OFT_GLASS_LINUX_H
#define OFT_GLASS_LINUX_H

// oft_attach_x11_blur opens its own connection to the X server and sets
// _KDE_NET_WM_BLUR_BEHIND_REGION on the given window, requesting
// whole-window blur from KWin (or any compositor honouring the same
// hint). No-op, not an error, under any other window manager. window is
// an X11 Window id (XID), not a pointer — passed as unsigned long to
// match Xlib's own typedef.
void oft_attach_x11_blur(unsigned long window);

#endif
