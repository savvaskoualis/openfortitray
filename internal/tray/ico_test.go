package tray

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The Windows tray icon was invisible because raw PNGs were handed to an API
// that only loads ICO. pngToICO must produce a valid single-entry ICO whose
// payload is the original PNG.
func TestPNGToICO(t *testing.T) {
	src := padOrOriginal(iconGreen)
	ico, err := pngToICO(src)
	if err != nil {
		t.Fatal(err)
	}
	var hdr [3]uint16
	binary.Read(bytes.NewReader(ico[:6]), binary.LittleEndian, &hdr)
	if hdr != [3]uint16{0, 1, 1} {
		t.Fatalf("ICONDIR = %v, want [0 1 1]", hdr)
	}
	size := binary.LittleEndian.Uint32(ico[14:18])
	offset := binary.LittleEndian.Uint32(ico[18:22])
	if int(size) != len(src) || offset != 22 {
		t.Fatalf("entry size/offset = %d/%d, want %d/22", size, offset, len(src))
	}
	if !bytes.Equal(ico[offset:], src) {
		t.Error("ICO payload is not the original PNG")
	}
	if ico[6] == 0 || ico[6] != ico[7] {
		t.Errorf("entry dims = %dx%d, want the padded square size", ico[6], ico[7])
	}
}

func TestPNGToICORejectsGarbage(t *testing.T) {
	if _, err := pngToICO([]byte("not a png")); err == nil {
		t.Error("pngToICO accepted non-PNG input")
	}
}
