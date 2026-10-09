//go:build darwin

#import <Foundation/Foundation.h>
#import <UserNotifications/UserNotifications.h>
#include "notify_darwin.h"

// Implemented in Go (notify_darwin.go).
extern void oftNotificationClicked(void);

static volatile int oftNotifyGranted = 0;
static volatile int oftNotifyReady = 0;

@interface OFTNotifyDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation OFTNotifyDelegate
// Show the banner even while the app is frontmost (e.g. the status window is
// open); by default macOS silently drops notifications for the active app.
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))completionHandler {
  completionHandler(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList |
                    UNNotificationPresentationOptionSound);
}

- (void)userNotificationCenter:(UNUserNotificationCenter *)center
    didReceiveNotificationResponse:(UNNotificationResponse *)response
             withCompletionHandler:(void (^)(void))completionHandler {
  if ([response.actionIdentifier isEqualToString:UNNotificationDefaultActionIdentifier]) {
    oftNotificationClicked();
  }
  completionHandler();
}
@end

static OFTNotifyDelegate *oftNotifyDelegate;

int oft_notify_init(void) {
  if ([[NSBundle mainBundle] bundleIdentifier] == nil) {
    return 0;
  }
  UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
  oftNotifyDelegate = [[OFTNotifyDelegate alloc] init];
  center.delegate = oftNotifyDelegate;
  [center requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
                        completionHandler:^(BOOL granted, NSError *error) {
                          if (error != nil) {
                            NSLog(@"openfortitray: notification permission: %@", error);
                          }
                          oftNotifyGranted = granted ? 1 : 0;
                        }];
  oftNotifyReady = 1;
  return 1;
}

int oft_notify_post(const char *title, const char *body) {
  if (!oftNotifyReady || !oftNotifyGranted) {
    return 0;
  }
  UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
  content.title = [NSString stringWithUTF8String:title];
  content.body = [NSString stringWithUTF8String:body];
  content.sound = [UNNotificationSound defaultSound];
  UNNotificationRequest *req =
      [UNNotificationRequest requestWithIdentifier:[[NSUUID UUID] UUIDString] content:content trigger:nil];
  [[UNUserNotificationCenter currentNotificationCenter]
      addNotificationRequest:req
       withCompletionHandler:^(NSError *error) {
         if (error != nil) {
           NSLog(@"openfortitray: notification post failed: %@", error);
         }
       }];
  return 1;
}
