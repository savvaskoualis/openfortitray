//go:build darwin

#import <Cocoa/Cocoa.h>
#include "glass_darwin.h"

static NSString *const oftGlassIdentifier = @"oft-glass";

void oft_attach_glass(uintptr_t nsviewPtr) {
  NSView *qtView = (NSView *)nsviewPtr;
  NSWindow *window = qtView.window;
  if (window == nil) {
    return;
  }

  NSView *current = window.contentView;
  if (current == nil) {
    return;
  }

  // Already wrapped by a previous call: `current` IS the wrapper this
  // function installs below, identifiable by its first child being the
  // tagged glass view. Just keep it sized to match (the window may have
  // been resized since) and stop — Reveal() calls this on every window
  // show, so without this check each reveal would wrap (and leak) another
  // layer.
  if (current.subviews.count > 0 &&
      [current.subviews[0].identifier isEqualToString:oftGlassIdentifier]) {
    current.subviews[0].frame = current.bounds;
    return;
  }

  // Deliberately NOT setting window.titlebarAppearsTransparent: the
  // content view (and our glass view sized to match it) sits BELOW the
  // titlebar, not behind it — a transparent titlebar with nothing drawn
  // there just shows whatever's on the desktop behind the window, raw and
  // unblurred, right above our actually-blurred content. That read as a
  // glitchy mismatched strip in practice, not a unified look. The
  // titlebar's own native (opaque) material — the same one every other
  // vibrant macOS app's title bar uses above its blurred sidebar/content —
  // is the correct, consistent choice here.

  // `current` here is the Qt-backed rendering view — NOT a plain container.
  // Adding the glass view as a subview OF IT does not put glass behind
  // Qt's rendered pixels: a view's own drawing is not itself a sibling layer
  // that positioning can slot under. The fix is to make glass and Qt's view
  // true siblings: wrap both in a new plain NSView and install THAT as
  // window.contentView, with glass added first (bottom) and Qt's original
  // view added second (top, full bounds). Sibling views composite correctly,
  // and a plain wrapper with a full-bounds front child hit-tests straight
  // through to that front child (AppKit's default hitTest: recurses into
  // subviews before matching itself), so mouse/keyboard input keeps reaching
  // Qt's view exactly as before.
  NSView *originalContent = current;

  NSVisualEffectView *glass =
      [[NSVisualEffectView alloc] initWithFrame:originalContent.bounds];
  glass.identifier = oftGlassIdentifier;
  glass.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
  glass.blendingMode = NSVisualEffectBlendingModeBehindWindow;
  glass.material = NSVisualEffectMaterialMenu;
  glass.state = NSVisualEffectStateActive;

  NSView *wrapper = [[NSView alloc] initWithFrame:originalContent.frame];
  wrapper.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;

  originalContent.frame = wrapper.bounds;
  originalContent.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;

  [wrapper addSubview:glass];
  [wrapper addSubview:originalContent];

  window.contentView = wrapper;
}
