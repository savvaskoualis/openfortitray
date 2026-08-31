#import <Cocoa/Cocoa.h>
#include "glass_darwin.h"

// This app has exactly one window that ever needs glass, so a pair of
// static globals (rather than a general keyed registry) is the simplest
// correct thing — see the design note in oft_attach_glass below for why
// this exists as a SEPARATE window instead of manipulating the main
// window's own view hierarchy.
static NSWindow *gMainWindow = nil;
static NSWindow *gGlassWindow = nil;

// OFTGlassSync keeps the glass window's frame glued to the main window's
// on every move/resize. A plain C function can't be an NSNotificationCenter
// selector target, hence this tiny observer object.
@interface OFTGlassSync : NSObject
- (void)syncFrame:(NSNotification *)note;
@end

@implementation OFTGlassSync
- (void)syncFrame:(NSNotification *)note {
  if (gMainWindow == nil || gGlassWindow == nil) {
    return;
  }
  [gGlassWindow setFrame:gMainWindow.frame display:YES];
}
@end

static OFTGlassSync *gSync = nil;

// oft_attach_glass adds native macOS vibrancy WITHOUT touching Qt's own
// window or view hierarchy at all.
//
// An earlier version of this function made Qt's content view a sibling of
// an NSVisualEffectView by replacing window.contentView with a new plain
// NSView wrapping both — the same technique that worked for the Fyne-based
// implementation this app used before migrating to Qt. Confirmed live that
// this breaks Qt's own macOS repaint: once Qt's content view is no longer
// literally window.contentView, switching the app's QStackedWidget page
// (Status -> Connection -> Advanced) stopped repainting anything at all.
//
// Instead, this creates a SEPARATE borderless, vibrant NSWindow the exact
// size of the main window, attaches it as a child window ordered BEHIND
// the main window, and keeps its frame synced on every move/resize. Qt's
// window and view objects are never touched — this sidesteps the repaint
// bug by construction, at the cost of needing to keep two windows in sync
// rather than one. This is a well-established pattern for adding vibrancy
// behind a window whose own content view can't safely be restructured.
void oft_attach_glass(uintptr_t nsviewPtr) {
  NSView *qtView = (NSView *)nsviewPtr;
  NSWindow *window = qtView.window;
  if (window == nil) {
    return;
  }

  // Idempotent: Reveal() calls this on every window show. If already
  // attached to this window, just make sure the frame is current and stop.
  if (gGlassWindow != nil && gMainWindow == window) {
    [gGlassWindow setFrame:window.frame display:YES];
    return;
  }

  window.opaque = NO;
  window.backgroundColor = [NSColor clearColor];

  NSWindow *glass =
      [[NSWindow alloc] initWithContentRect:window.frame
                                   styleMask:NSWindowStyleMaskBorderless
                                     backing:NSBackingStoreBuffered
                                       defer:NO];
  glass.releasedWhenClosed = NO;
  glass.opaque = NO;
  glass.backgroundColor = [NSColor clearColor];
  glass.hasShadow = NO;
  glass.ignoresMouseEvents = YES;
  // Below the main window in z-order but still a normal-level window (not
  // desktop-level), so it stays correctly stacked against other apps.
  glass.level = window.level;

  NSVisualEffectView *effect =
      [[NSVisualEffectView alloc] initWithFrame:glass.contentView.bounds];
  effect.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
  effect.material = NSVisualEffectMaterialMenu;
  effect.blendingMode = NSVisualEffectBlendingModeBehindWindow;
  effect.state = NSVisualEffectStateActive;
  glass.contentView = effect;

  gMainWindow = window;
  gGlassWindow = glass;

  [window addChildWindow:glass ordered:NSWindowBelow];
  [glass orderFront:nil];

  if (gSync == nil) {
    gSync = [[OFTGlassSync alloc] init];
  }
  [[NSNotificationCenter defaultCenter] addObserver:gSync
                                            selector:@selector(syncFrame:)
                                                name:NSWindowDidResizeNotification
                                              object:window];
  [[NSNotificationCenter defaultCenter] addObserver:gSync
                                            selector:@selector(syncFrame:)
                                                name:NSWindowDidMoveNotification
                                              object:window];
}
