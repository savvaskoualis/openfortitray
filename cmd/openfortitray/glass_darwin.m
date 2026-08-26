//go:build darwin

#import <Cocoa/Cocoa.h>
#include "glass_darwin.h"

static NSString *const oftGlassIdentifier = @"oft-glass";

void oft_attach_glass(uintptr_t nswindowPtr) {
  NSWindow *window = (__bridge NSWindow *)(void *)nswindowPtr;
  if (window == nil) {
    return;
  }

  // window.opaque/backgroundColor are already set by GLFW itself when the
  // TransparentFramebuffer hint is on (see go-gl/glfw's cocoa_window.m,
  // createNativeWindow) — nothing to add here.

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

  window.titlebarAppearsTransparent = YES;

  // `current` here is Fyne's own GL-backed rendering view (GLFW installs it
  // directly as window.contentView) — NOT a plain container. Adding the
  // glass view as a subview OF IT, even "positioned below", does not put
  // glass behind Fyne's rendered pixels: an OpenGL view's own drawing is
  // not itself a sibling layer that positioning can slot under, so a
  // subview added there either sits fully on top (obscuring everything,
  // confirmed live: a totally blank window) or fully underneath (no visible
  // blur, confirmed in this project's own throwaway prototype) depending on
  // exactly how AppKit backs the view — never the two composited together.
  //
  // The fix is to make glass and Fyne's view true siblings: wrap both in a
  // new plain NSView and install THAT as window.contentView, with glass
  // added first (bottom) and Fyne's original view added second (top, full
  // bounds). Sibling views composite correctly, and a plain wrapper with a
  // full-bounds front child hit-tests straight through to that front child
  // (AppKit's default hitTest: recurses into subviews before matching
  // itself), so mouse/keyboard input keeps reaching Fyne's view exactly as
  // before.
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
