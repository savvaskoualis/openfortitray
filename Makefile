BIN  := openfortitray
DIST := dist
PKG  := ./cmd/openfortitray

# macOS .app bundle (Task 10). The bundle id MUST match app.NewWithID in
# cmd/openfortitray/main.go and the launchd Label in internal/autostart, or the
# LaunchAgent and the running app disagree about who they are.
APP        := OpenFortiTray
APP_BUNDLE := $(DIST)/$(APP).app
APP_PLIST  := scripts/Info.plist
ICON_SRC   := assets/icons/gate_dock.svg

.PHONY: all build test release clean install app

all: build

build:
	go build -o $(BIN) $(PKG)

test:
	go vet ./...
	go test -race ./...

# Cross-compilation notes:
#  - darwin needs cgo: fyne.io/systray drives the Cocoa status bar. The amd64
#    slice cross-builds from an Apple Silicon mac because the macOS SDK is a
#    fat SDK; if a future SDK drops x86_64 support, drop that line — the amd64
#    slice only serves pre-2020 Intel macs.
#  - linux systray is pure Go (D-Bus StatusNotifierItem via godbus), so it
#    builds with cgo off and stays portable across glibc versions.
#  - windows needs -H=windowsgui, or launching the tray app pops a console
#    window alongside it.
release: clean
	mkdir -p $(DIST)
	CGO_ENABLED=1 GOOS=darwin  GOARCH=arm64 go build -o $(DIST)/$(BIN)-darwin-arm64 $(PKG)
	CGO_ENABLED=1 GOOS=darwin  GOARCH=amd64 go build -o $(DIST)/$(BIN)-darwin-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o $(DIST)/$(BIN)-linux-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-H=windowsgui" -o $(DIST)/$(BIN)-windows-amd64.exe $(PKG)
	@ls -l $(DIST)

# app assembles a hand-rolled macOS .app bundle (Task 10). A fyne menu-bar app
# needs a real bundle with LSUIElement=1 for the status item to render reliably
# and to keep the process off the Dock. Idempotent: the bundle is rebuilt from
# scratch each time. macOS only (iconutil/sips and the Cocoa systray are Darwin).
app: build
ifneq ($(shell uname -s),Darwin)
	@echo "make app: macOS only (this is $(shell uname -s)); skipping" && exit 1
endif
	rm -rf $(APP_BUNDLE)
	mkdir -p $(APP_BUNDLE)/Contents/MacOS $(APP_BUNDLE)/Contents/Resources
	cp $(BIN) $(APP_BUNDLE)/Contents/MacOS/$(BIN)
	cp $(APP_PLIST) $(APP_BUNDLE)/Contents/Info.plist
	@if command -v iconutil >/dev/null 2>&1 && command -v rsvg-convert >/dev/null 2>&1; then \
		set -e; \
		work="$$(mktemp -d)"; iconset="$$work/AppIcon.iconset"; mkdir -p "$$iconset"; \
		for sz in 16 32 128 256 512; do \
			rsvg-convert -w $$sz         -h $$sz         "$(ICON_SRC)" -o "$$iconset/icon_$${sz}x$${sz}.png"; \
			rsvg-convert -w $$((sz*2))   -h $$((sz*2))   "$(ICON_SRC)" -o "$$iconset/icon_$${sz}x$${sz}@2x.png"; \
		done; \
		iconutil -c icns "$$iconset" -o "$(APP_BUNDLE)/Contents/Resources/AppIcon.icns"; \
		rm -rf "$$work"; \
		echo "make app: generated Contents/Resources/AppIcon.icns from $(ICON_SRC)"; \
	else \
		echo "make app: iconutil/rsvg-convert not found — skipping .icns (not a blocker)"; \
	fi
	@echo "make app: assembled $(APP_BUNDLE)"

# Installs on this machine (macOS/Linux): openconnect, binary, privileged
# helper, sudoers rule. Prompts for sudo.
install:
	bash scripts/install.sh

clean:
	rm -rf $(DIST) $(BIN)
