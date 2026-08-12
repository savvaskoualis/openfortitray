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

# VERSION stamps the .dmg filename. Locally it derives from git (a tag when on
# one, else the short SHA); CI overrides it with the release tag
# (make dmg VERSION=$GITHUB_REF_NAME). `?=` so the CI override wins.
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
DMG     := $(DIST)/$(APP)-$(VERSION).dmg

.PHONY: all build test release clean install app dmg

all: build

build:
	go build -o $(BIN) $(PKG)

test:
	go vet ./...
	go test -race ./...

# Size trim for release builds. fyne statically links the GL bindings, a font
# shaper and the default theme/font, so a release binary is ~15-30 MB heavier
# than the old systray one; -s -w (strip symbol table + DWARF) claws some back.
LDFLAGS_TRIM := -s -w

# Build/CI reality since the fyne v2 migration: fyne renders via OpenGL/GLFW, so
# cmd/openfortitray is a cgo build on EVERY OS. That kills the old pure
# cross-compile model (CGO_ENABLED=0 for linux/windows from any host). Each OS
# must now build on its own native toolchain:
#   - darwin: cgo via the Xcode CLT. The amd64 slice still cross-builds from an
#     Apple Silicon mac because the macOS SDK is a fat SDK; if a future SDK
#     drops x86_64, delete that line — it only serves pre-2020 Intel macs.
#   - linux: cgo needs gcc + GL/X11 dev headers (libgl1-mesa-dev xorg-dev).
#   - windows: cgo needs a MinGW gcc; -H=windowsgui suppresses the console
#     window. Cannot be cross-built from a non-windows host without a MinGW
#     cross-toolchain.
#
# Consequently a LOCAL `make release` can only build what THIS host's toolchain
# supports: on macOS both darwin slices (arm64 native + amd64 via the fat SDK),
# on linux the linux binary, on windows the windows exe. The full three-OS
# matrix is produced by CI (.github/workflows/release.yml), one native runner
# per OS. This target builds the host-appropriate subset and says so.
release: clean
	mkdir -p $(DIST)
ifeq ($(shell uname -s),Darwin)
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS_TRIM)" -o $(DIST)/$(BIN)-darwin-arm64 $(PKG)
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS_TRIM)" -o $(DIST)/$(BIN)-darwin-amd64 $(PKG)
	@file $(DIST)/$(BIN)-darwin-arm64 | grep -q 'arm64'
	@file $(DIST)/$(BIN)-darwin-amd64 | grep -q 'x86_64'
	@echo "make release: built darwin arm64 + amd64. linux/windows come from CI (native runners)."
else ifeq ($(shell uname -s),Linux)
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS_TRIM)" -o $(DIST)/$(BIN)-linux-amd64 $(PKG)
	@echo "make release: built linux amd64. darwin/windows come from CI (native runners)."
else
	CGO_ENABLED=1 GOARCH=amd64 go build -ldflags="$(LDFLAGS_TRIM) -H=windowsgui" -o $(DIST)/$(BIN)-windows-amd64.exe $(PKG)
	@echo "make release: built windows amd64. darwin/linux come from CI (native runners)."
endif
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
# Ad-hoc codesign LAST — the bundle is now fully assembled (binary, Info.plist,
# .icns all in place), and any change to the bundle after signing invalidates the
# signature. An UNSIGNED Mach-O (the CI build lands with Identifier=a.out and no
# signature) is killed by the OS on Apple Silicon even after quarantine is
# stripped — "damaged / can't be opened". `-s -` is an ad-hoc identity: no
# Developer-ID cert, no paid Apple account, but a valid signature the kernel
# accepts, so the app runs. It is NOT notarized, so a browser-downloaded .dmg
# still shows the "unidentified developer" prompt; a `brew install --cask` install
# strips quarantine automatically and gets a no-prompt launch. --deep signs the
# nested code; no entitlements are needed for this app. Guarded so a codesign-less
# environment (non-macOS) does not break `make app`; on macOS codesign always ships
# with the CLT. `make dmg` depends on `app` and stages the bundle only after this
# target completes, so the shipped .dmg carries the SIGNED bundle.
	@if command -v codesign >/dev/null 2>&1; then \
		codesign --force --deep -s - "$(APP_BUNDLE)" && \
		echo "make app: ad-hoc signed $(APP_BUNDLE)"; \
	else \
		echo "make app: codesign not available, skipping (unsigned)"; \
	fi

# dmg wraps the .app in a double-click, drag-to-Applications disk image — the
# primary macOS download. Depends on `app`, so the bundle is fresh. macOS only
# (hdiutil/create-dmg are Darwin). Idempotent: the target rm's the image first.
#
# Two builders, both yielding a dmg with OpenFortiTray.app beside an
# /Applications symlink:
#   - create-dmg (the create-dmg/create-dmg Homebrew formula, `brew install
#     create-dmg`) when present: adds the drop-link and lays out the window
#     (icon positions, sizes) via Finder/AppleScript for the polished look.
#   - hdiutil otherwise (and if create-dmg fails — it drives Finder over
#     AppleScript, which has no GUI session on a CI runner and errors there):
#     stage the .app + `ln -s /Applications`, then `hdiutil create ... UDZO`.
#     Plainer window, identical drag-install behaviour. CI deliberately runs
#     this path (it does not install create-dmg) for headless reliability.
dmg: app
ifneq ($(shell uname -s),Darwin)
	@echo "make dmg: macOS only (this is $(shell uname -s)); skipping" && exit 1
endif
	rm -f "$(DMG)"
	@stage="$$(mktemp -d)"; \
	cp -R "$(APP_BUNDLE)" "$$stage/"; \
	if command -v create-dmg >/dev/null 2>&1; then \
		echo "make dmg: using create-dmg for a laid-out drag-install window"; \
		if create-dmg \
			--volname "$(APP)" \
			--window-pos 200 120 \
			--window-size 640 400 \
			--icon-size 128 \
			--icon "$(APP).app" 160 200 \
			--app-drop-link 480 200 \
			--no-internet-enable \
			"$(DMG)" "$$stage"; then \
			:; \
		else \
			echo "make dmg: create-dmg failed (likely no GUI session) — falling back to hdiutil"; \
			rm -f "$(DMG)"; \
			ln -s /Applications "$$stage/Applications"; \
			hdiutil create -volname "$(APP)" -srcfolder "$$stage" -ov -format UDZO "$(DMG)"; \
		fi; \
	else \
		echo "make dmg: create-dmg not found — using hdiutil (plain drag-install layout)"; \
		ln -s /Applications "$$stage/Applications"; \
		hdiutil create -volname "$(APP)" -srcfolder "$$stage" -ov -format UDZO "$(DMG)"; \
	fi; \
	rm -rf "$$stage"
	@echo "make dmg: built $(DMG)"
	@ls -l "$(DMG)"

# Installs on this machine (macOS/Linux): openconnect, binary, privileged
# helper, sudoers rule. Prompts for sudo.
install:
	bash scripts/install.sh

clean:
	rm -rf $(DIST) $(BIN)
