#import <Cocoa/Cocoa.h>
#include "cursor_darwin.h"

int oft_cursor_position(double *x, double *y) {
    NSArray<NSScreen *> *screens = [NSScreen screens];
    if (screens.count == 0) {
        return 0;
    }
    // screens[0] is guaranteed by AppKit to be the screen containing the
    // menu bar (the "primary" screen), whose frame origin is (0,0) in
    // AppKit's global, bottom-left-origin coordinate space -- exactly the
    // reference frame this converts into: a top-left-origin position
    // relative to that same screen, which is what Wails' own
    // WindowSetPosition expects (confirmed directly against Wails'
    // WailsContext.m: SetPosition flips a top-left-origin (x,y) into
    // AppKit's native frame internally, so the caller must NOT do that
    // flip itself -- only the bottom-left-to-top-left origin conversion
    // below, which is a different thing).
    NSScreen *primary = screens[0];
    NSPoint loc = [NSEvent mouseLocation];
    *x = loc.x - primary.frame.origin.x;
    *y = primary.frame.size.height - (loc.y - primary.frame.origin.y);
    return 1;
}
