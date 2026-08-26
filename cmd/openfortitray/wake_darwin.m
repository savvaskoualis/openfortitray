//go:build darwin

#import <Cocoa/Cocoa.h>
#include "wake_darwin.h"

// Implemented in Go (wake_darwin.go) and exported to C.
extern void oftSystemWoke(void);

void oft_watch_wake(void) {
  // NSWorkspace's own notification center, not NSNotificationCenter's default
  // one (which is where NSApplicationDidBecomeActiveNotification — the Dock
  // activation hook — is posted): system-level events like sleep/wake and
  // screen lock are posted there instead.
  //
  // The block runs on the main queue, so the Go callback is already on the
  // thread AppKit requires, matching oft_watch_activation in dock_darwin.m.
  [[[NSWorkspace sharedWorkspace] notificationCenter]
      addObserverForName:NSWorkspaceDidWakeNotification
                  object:nil
                   queue:[NSOperationQueue mainQueue]
              usingBlock:^(NSNotification *note) {
                oftSystemWoke();
              }];
}
