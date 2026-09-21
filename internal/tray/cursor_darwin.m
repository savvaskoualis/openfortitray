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

int oft_cursor_screen_frame(double *originX, double *originY, double *width, double *height) {
    NSArray<NSScreen *> *screens = [NSScreen screens];
    if (screens.count == 0) {
        return 0;
    }
    NSScreen *primary = screens[0];
    NSPoint loc = [NSEvent mouseLocation];

    NSScreen *target = primary;
    for (NSScreen *s in screens) {
        if (NSMouseInRect(loc, s.frame, NO)) {
            target = s;
            break;
        }
    }

    // Convert target's TOP-LEFT corner (AppKit's bottom-left-origin global
    // space has that at (origin.x, origin.y + height), since y increases
    // upward there) into primary-relative, top-left-origin space -- the
    // exact same conversion oft_cursor_position applies to the cursor point
    // itself, so callers can compare the two directly.
    *originX = target.frame.origin.x - primary.frame.origin.x;
    *originY = primary.frame.size.height -
        ((target.frame.origin.y + target.frame.size.height) - primary.frame.origin.y);
    *width = target.frame.size.width;
    *height = target.frame.size.height;
    return 1;
}

int oft_set_window_position(const char *title, double topLeftX, double topLeftY) {
    NSArray<NSScreen *> *screens = [NSScreen screens];
    if (screens.count == 0) {
        return 0;
    }
    NSScreen *primary = screens[0];
    NSString *wantTitle = [NSString stringWithUTF8String:title];

    for (NSWindow *w in [NSApp windows]) {
        if (![w.title isEqualToString:wantTitle]) {
            continue;
        }
        // setFrameOrigin: takes the window's BOTTOM-LEFT corner, in
        // AppKit's bottom-left-origin absolute space -- convert from the
        // caller's top-left-of-primary-screen frame the same way
        // oft_cursor_position does, then subtract the window's own height
        // to go from "top edge" to "bottom-left corner". This exact
        // formula is duplicated (and unit-tested) as tray.MacOSFrameOrigin
        // in position.go/position_test.go -- keep both in sync by hand if
        // this ever changes.
        NSRect f = w.frame;
        double bottomLeftX = primary.frame.origin.x + topLeftX;
        double bottomLeftY = primary.frame.origin.y + primary.frame.size.height - topLeftY - f.size.height;
        [w setFrameOrigin:NSMakePoint(bottomLeftX, bottomLeftY)];
        // AppKit auto-generates its own drop shadow following the window's
        // real (square) frame, since Frameless doesn't turn that off by
        // itself. With WebviewIsTransparent making everything outside the
        // CSS card's rounded corners see-through, that square native shadow
        // shows through past the rounded corners -- worst at the bottom,
        // where a downward-biased shadow is strongest. The CSS box-shadow on
        // .page is the only shadow this window should have.
        [w setHasShadow:NO];
        return 1;
    }
    return 0;
}
