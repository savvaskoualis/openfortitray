package tray

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/png"
)

// pngToICO wraps PNG bytes in a single-image ICO container (the PNG-in-ICO
// form Windows has accepted since Vista). The Windows systray loads its icon
// with LoadImage(IMAGE_ICON, LR_LOADFROMFILE), which only understands ICO —
// handed raw PNG bytes it fails and the tray icon is invisible.
func pngToICO(data []byte) ([]byte, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width > 256 || cfg.Height > 256 {
		return nil, errors.New("ico: image larger than 256px")
	}
	dim := func(n int) byte { // 256 is stored as 0
		if n == 256 {
			return 0
		}
		return byte(n)
	}
	var b bytes.Buffer
	// ICONDIR: reserved, type 1 (icon), 1 image.
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, 1})
	// ICONDIRENTRY.
	b.Write([]byte{dim(cfg.Width), dim(cfg.Height), 0, 0})
	binary.Write(&b, binary.LittleEndian, uint16(1))  // color planes
	binary.Write(&b, binary.LittleEndian, uint16(32)) // bits per pixel
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	binary.Write(&b, binary.LittleEndian, uint32(6+16)) // offset past header+entry
	b.Write(data)
	return b.Bytes(), nil
}
