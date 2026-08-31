//go:build darwin

#import <Cocoa/Cocoa.h>
#include "dock_darwin.h"

// Implemented in Go (dockpolicy_darwin.go) and exported to C.
extern void oftDockActivated(void);

void oft_set_regular_policy(void) {
  if (NSApp == nil) {
    return;
  }
  [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
}

void oft_watch_activation(void) {
  // Why a notification rather than the obvious delegate method: clicking a Dock
  // icon when every window is hidden sends
  // applicationShouldHandleReopen:hasVisibleWindows:, but Qt's Cocoa platform
  // plugin owns NSApp's delegate and does not implement it — so with no
  // handler the Dock icon is inert and clicking it does nothing at all.
  //
  // Replacing the delegate would take that ownership away from Qt and break
  // its own event handling. Observing NSApplicationDidBecomeActiveNotification
  // needs no delegate: it fires on a Dock click, on Cmd-Tab, and on any other
  // activation, which is the same intent in every case — "bring this app up".
  //
  // The block runs on the main queue, so the Go callback is already on the thread
  // AppKit requires.
  [[NSNotificationCenter defaultCenter]
      addObserverForName:NSApplicationDidBecomeActiveNotification
                  object:nil
                   queue:[NSOperationQueue mainQueue]
              usingBlock:^(NSNotification *note) {
                oftDockActivated();
              }];
}
