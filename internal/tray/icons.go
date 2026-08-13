package tray

import _ "embed"

//go:embed assets/icon_gray.png
var iconGray []byte // disconnected

//go:embed assets/icon_green.png
var iconGreen []byte // connected

//go:embed assets/icon_yellow.png
var iconYellow []byte // authenticating / connecting / reconnecting

//go:embed assets/icon_red.png
var iconRed []byte // error
