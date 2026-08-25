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

  NSVisualEffectView *glass =
      [[NSVisualEffectView alloc] initWithFrame:contentView.bounds];
  glass.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
  glass.blendingMode = NSVisualEffectBlendingModeBehindWindow;
  glass.material = NSVisualEffectMaterialMenu;
  glass.state = NSVisualEffectStateActive;

  [contentView addSubview:glass positioned:NSWindowBelow relativeTo:nil];
}
