#ifndef OFT_GLASS_DARWIN_H
#define OFT_GLASS_DARWIN_H

#include <stdint.h>

// oft_attach_glass inserts a real NSVisualEffectView (material: Menu, the
// medium-tint/high-saturation material closest to macOS's own Liquid
// Glass look) behind the given widget's content, so a translucent
// ColorNameBackground (see internal/uitheme) lets it show through.
//
// nsviewPtr is the result of Qt's QWidget::WinId() on macOS, which returns
// an NSView pointer. It is taken as uintptr_t to avoid unsafe.Pointer
// conversions on the Go side (which trip go vet's unsafeptr check).
void oft_attach_glass(uintptr_t nsviewPtr);

#endif
