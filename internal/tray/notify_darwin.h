#ifndef OFT_NOTIFY_DARWIN_H
#define OFT_NOTIFY_DARWIN_H

// oft_notify_init installs the UNUserNotificationCenter delegate and requests
// alert permission. Returns 0 (and does nothing) when the process has no
// bundle identifier — a bare binary or a `go test` run — where touching
// UNUserNotificationCenter raises an Objective-C exception.
int oft_notify_init(void);

// oft_notify_post posts a native notification. Returns 1 if it was handed to
// Notification Center, 0 if native notifications are unavailable (no bundle,
// or permission denied), so the caller can fall back.
int oft_notify_post(const char *title, const char *body);

#endif
