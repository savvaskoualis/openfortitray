//go:build darwin

#import <Cocoa/Cocoa.h>
#include "glass_darwin.h"

void oft_attach_glass(uintptr_t nswindowPtr) {
  NSWindow *window = (__bridge NSWindow *)(void *)nswindowPtr;
  if (window == nil) {
    return;
  }

  window.opaque = NO;
  window.backgroundColor = [NSColor clearColor];
  window.titlebarAppearsTransparent = YES;

  NSView *contentView = window.contentView;
  if (contentView == nil) {
    return;
  }

  static NSString *const oftGlassIdentifier = @"oft-glass";

  for (NSView *existing in contentView.subviews) {
    if ([existing.identifier isEqualToString:oftGlassIdentifier]) {
      // Already attached to this content view: just keep it sized to match
      // (the window may have been resized since), and skip allocating a
      // second one. Reveal() calls this on every window show, so without
      // this check each reveal would stack another full-bounds view.
      existing.frame = contentView.bounds;
      return;
    }
  }

  NSVisualEffectView *glass =
      [[NSVisualEffectView alloc] initWithFrame:contentView.bounds];
  glass.identifier = oftGlassIdentifier;
  glass.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
  glass.blendingMode = NSVisualEffectBlendingModeBehindWindow;
  glass.material = NSVisualEffectMaterialMenu;
  glass.state = NSVisualEffectStateActive;

  [contentView addSubview:glass positioned:NSWindowBelow relativeTo:nil];
}
