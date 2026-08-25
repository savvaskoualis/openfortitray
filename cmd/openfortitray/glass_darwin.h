#ifndef OFT_GLASS_DARWIN_H
#define OFT_GLASS_DARWIN_H

#include <stdint.h>

// oft_attach_glass inserts a real NSVisualEffectView (material: Menu, the
// medium-tint/high-saturation material closest to macOS's own Liquid
// Glass look) behind the given NSWindow's content, so a translucent
// ColorNameBackground (see internal/uitheme) lets it show through.
//
// nswindow is taken as uintptr_t (Fyne's driver.MacWindowContext.NSWindow
// is a uintptr) rather than void*, and cast to a pointer here in
// Objective-C instead of in Go: converting a bare uintptr straight to
// unsafe.Pointer on the Go side trips `go vet`'s unsafeptr check.
void oft_attach_glass(uintptr_t nswindow);

#endif
