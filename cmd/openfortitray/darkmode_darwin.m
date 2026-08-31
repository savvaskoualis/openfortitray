#import <Cocoa/Cocoa.h>
#include "darkmode_darwin.h"

int oft_is_dark_mode(void) {
  NSString *style = [[NSUserDefaults standardUserDefaults]
      stringForKey:@"AppleInterfaceStyle"];
  return [style isEqualToString:@"Dark"] ? 1 : 0;
}
