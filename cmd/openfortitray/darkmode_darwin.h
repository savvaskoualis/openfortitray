#ifndef OFT_DARKMODE_DARWIN_H
#define OFT_DARKMODE_DARWIN_H

// oft_is_dark_mode returns 1 if the current user's macOS appearance is Dark,
// 0 otherwise (including "no preference set", which macOS treats as Light).
int oft_is_dark_mode(void);

#endif
