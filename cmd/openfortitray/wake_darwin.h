#ifndef OFT_WAKE_DARWIN_H
#define OFT_WAKE_DARWIN_H

// oft_watch_wake subscribes to NSWorkspaceDidWakeNotification and calls the
// exported Go function oftSystemWoke on the main queue each time the system
// resumes from sleep.
void oft_watch_wake(void);

// oft_watch_screen_wake subscribes to NSWorkspaceScreensDidWakeNotification
// and calls the exported Go function oftScreenWoke on the main queue each
// time the display wakes — including a display-only sleep/wake cycle that
// never triggers a full NSWorkspaceDidWakeNotification (e.g. a Mac whose
// power settings, AC power, or an active caffeinate/other assertion keep it
// from ever fully suspending).
void oft_watch_screen_wake(void);

#endif
