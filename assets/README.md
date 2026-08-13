# Icon assets

`assets/icons/openfortitray_{gray,green,yellow,red}.svg` are the masters: a 16×16
viewBox drawing of an arched openfortitray gate, one file per tray state.

| state file | tray state |
| --- | --- |
| `openfortitray_gray.svg` | disconnected |
| `openfortitray_yellow.svg` | authenticating / connecting / reconnecting |
| `openfortitray_green.svg` | connected |
| `openfortitray_red.svg` | error |

The tray does not read these. `internal/tray/icons.go` embeds
`internal/tray/assets/icon_{gray,green,yellow,red}.png` — 32×32 RGBA renders of
the matching SVG, which is the size systray wants for a Retina/HiDPI menu bar
(macOS and most Linux panels downscale to 16pt).

To change an icon, edit the SVG here and re-render its PNG at 32×32, e.g.

```sh
rsvg-convert -w 32 -h 32 assets/icons/openfortitray_gray.svg -o internal/tray/assets/icon_gray.png
```

Keep the PNG filenames as they are: they are named in the `//go:embed`
directives, and the build fails if one goes missing.
