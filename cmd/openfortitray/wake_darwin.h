#ifndef OFT_WAKE_DARWIN_H
#define OFT_WAKE_DARWIN_H

// oft_watch_wake subscribes to NSWorkspaceDidWakeNotification and calls the
// exported Go function oftSystemWoke on the main queue each time the system
// resumes from sleep.
void oft_watch_wake(void);

#endif
