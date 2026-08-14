#ifndef OFT_DOCK_DARWIN_H
#define OFT_DOCK_DARWIN_H

// oft_set_regular_policy puts the shared application in the Regular activation
// policy: a Dock icon and an app menu, alongside the menu-bar status item.
void oft_set_regular_policy(void);

// oft_watch_activation subscribes to NSApplicationDidBecomeActiveNotification and
// calls the exported Go function oftDockActivated on the main queue each time.
void oft_watch_activation(void);

#endif
