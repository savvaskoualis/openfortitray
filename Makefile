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
# Raster source for the .icns fallback: a stock macOS/CI runner has no
# rsvg-convert, but ships sips + iconutil, so the bundle icon is built from this
# 256px PNG when rsvg is absent (see the `app` target).
ICON_PNG   := assets/icons/openfortitray-256.png
# Windows exe icon resource. Committed .ico (real asset, not a build artifact —
# see .gitattributes `*.ico binary`); embedded into the exe via rsrc -ico below.
WIN_ICO    := assets/icons/openfortitray.ico

# VERSION stamps the .dmg filename. Locally it derives from git (a tag when on
# one, else the short SHA); CI overrides it with the release tag
# (make dmg VERSION=$GITHUB_REF_NAME). `?=` so the CI override wins.
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
DMG     := $(DIST)/$(APP)-$(VERSION).dmg

# Windows application manifest -> .syso resource. On Windows there is no
# privileged helper: the app runs openconnect directly, which needs Administrator
# to create the wintun adapter. openfortitray.manifest requests
# requireAdministrator so EVERY launch is elevated (see the file for the full
# rationale). The Go toolchain auto-links a GOOS/GOARCH-scoped *.syso found in the
# main package dir into the WINDOWS exe only — darwin/linux builds ignore it. We
# never commit the binary .syso (it is gitignored); it is regenerated from the
# checked-in manifest by `winres`. rsrc is pure Go (no cgo), so it cross-generates
# the COFF resource on any host; pinned to a full version like the CI actions.
RSRC       := github.com/akavel/rsrc@v0.10.2
MANIFEST   := $(PKG:./%=%)/openfortitray.manifest
SYSO_AMD64 := $(PKG:./%=%)/resource_windows_amd64.syso
SYSO_ARM64 := $(PKG:./%=%)/resource_windows_arm64.syso

.PHONY: all build test release clean install app dmg winres

all: build

# winres compiles the app manifest AND the .ico into the windows amd64 + arm64
# *.syso resources, so the exe both runs elevated (manifest) and carries a real
# application icon (-ico; previously the exe embedded only the manifest and
# showed the generic file icon in Explorer/taskbar). Safe (and a no-op for the
# output) on any host; the release/build recipes below invoke it only when the
# host is actually building windows.
winres:
	go run $(RSRC) -manifest $(MANIFEST) -ico $(WIN_ICO) -arch amd64 -o $(SYSO_AMD64)
	go run $(RSRC) -manifest $(MANIFEST) -ico $(WIN_ICO) -arch arm64 -o $(SYSO_ARM64)

build:
	@case "$$(uname -s)" in MINGW*|MSYS*|CYGWIN*|Windows*) $(MAKE) winres ;; esac
	CGO_CXXFLAGS=-std=c++17 go build -ldflags="$(LDFLAGS_VER)" -o $(BIN) $(PKG)

test:
	CGO_CXXFLAGS=-std=c++17 go vet ./...
	CGO_CXXFLAGS=-std=c++17 go test -race ./...

# Size trim for release builds: -s -w (strip symbol table + DWARF) trims the
# release binary.
LDFLAGS_TRIM := -s -w

# Stamp the build version into main.version (shown in the tray header). VERSION
# is the same value that names the .dmg (defined above; git describe locally,
# the tag in CI via `make ... VERSION=$GITHUB_REF_NAME`).
LDFLAGS_VER := -X main.version=$(VERSION)

# Build/CI reality: the UI is Qt6 via miqt, so cmd/openfortitray is a cgo
# build on EVERY OS. That kills the old pure cross-compile model
# (CGO_ENABLED=0 for linux/windows from any host). Each OS must now build on
# its own native toolchain:
#   - darwin: cgo via the Xcode CLT, arm64 only. Intel (amd64) macOS support
#     was dropped: it required a second, x86_64 Homebrew/Qt6 install
#     cross-built under Rosetta, and Homebrew has stopped shipping precompiled
#     Intel bottles for some of Qt6's own dependencies (confirmed live:
#     "openssl@3: no bottle available!", Tier 3/community-support-only) — the
#     x86_64 leg can no longer reliably build in CI at all, independent of
#     anything in this repo.
#   - linux: cgo needs gcc + the Qt6 dev headers (qt6-base-dev).
#   - windows: cgo needs a MinGW gcc; -H=windowsgui suppresses the console
#     window. Cannot be cross-built from a non-windows host without a MinGW
#     cross-toolchain.
#
# Consequently a LOCAL `make release` can only build what THIS host's toolchain
# supports: on macOS the darwin/arm64 binary, on linux the linux binary, on
# windows the windows exe. The full OS matrix is produced by CI
# (.github/workflows/release.yml), one native runner per OS. This target
# builds the host-appropriate subset and says so.
release: clean
	mkdir -p $(DIST)
ifeq ($(shell uname -s),Darwin)
	CGO_CXXFLAGS=-std=c++17 CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS_TRIM) $(LDFLAGS_VER)" -o $(DIST)/$(BIN)-darwin-arm64 $(PKG)
	@file $(DIST)/$(BIN)-darwin-arm64 | grep -q 'arm64'
	@echo "make release: built darwin arm64. linux/windows come from CI (native runners)."
else ifeq ($(shell uname -s),Linux)
	CGO_CXXFLAGS=-std=c++17 CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS_TRIM) $(LDFLAGS_VER)" -o $(DIST)/$(BIN)-linux-amd64 $(PKG)
	@echo "make release: built linux amd64. darwin/windows come from CI (native runners)."
else
	$(MAKE) winres
	CGO_CXXFLAGS=-std=c++17 CGO_ENABLED=1 GOARCH=amd64 go build -ldflags="$(LDFLAGS_TRIM) $(LDFLAGS_VER) -H=windowsgui" -o $(DIST)/$(BIN)-windows-amd64.exe $(PKG)
	@echo "make release: built windows amd64 (manifest embedded, runs elevated). darwin/linux come from CI (native runners)."
endif
	@ls -l $(DIST)

# app assembles a hand-rolled macOS .app bundle (Task 10). A menu-bar app
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
# Bundle the Qt6 runtime (miqt migration): unlike fyne's static Go binary, Qt6
# links against real shared libraries at runtime. macdeployqt copies the
# needed .framework bundles into Contents/Frameworks and rewrites the binary's
# load commands to find them there, so the .app runs on a machine without a
# matching Qt6 install. This MUST run before the codesign step below —
# codesigning after macdeployqt modifies the binary would invalidate the
# signature.
	@QT_PREFIX="$$(brew --prefix qt 2>/dev/null)"; \
	if [ -n "$$QT_PREFIX" ] && [ -x "$$QT_PREFIX/bin/macdeployqt" ]; then \
		"$$QT_PREFIX/bin/macdeployqt" "$(APP_BUNDLE)"; \
		echo "make app: bundled Qt6 frameworks via macdeployqt"; \
	else \
		echo "make app: macdeployqt not found at $$QT_PREFIX/bin — the .app will only run on machines with a matching Qt6 install" >&2; \
		exit 1; \
	fi
	@if command -v iconutil >/dev/null 2>&1 && command -v rsvg-convert >/dev/null 2>&1; then \
		set -e; \
		work="$$(mktemp -d)"; iconset="$$work/AppIcon.iconset"; mkdir -p "$$iconset"; \
		for sz in 16 32 128 256 512; do \
			rsvg-convert -w $$sz         -h $$sz         "$(ICON_SRC)" -o "$$iconset/icon_$${sz}x$${sz}.png"; \
			rsvg-convert -w $$((sz*2))   -h $$((sz*2))   "$(ICON_SRC)" -o "$$iconset/icon_$${sz}x$${sz}@2x.png"; \
		done; \
		iconutil -c icns "$$iconset" -o "$(APP_BUNDLE)/Contents/Resources/AppIcon.icns"; \
		rm -rf "$$work"; \
		echo "make app: generated Contents/Resources/AppIcon.icns from $(ICON_SRC) (rsvg)"; \
	elif command -v iconutil >/dev/null 2>&1 && command -v sips >/dev/null 2>&1; then \
		set -e; \
		work="$$(mktemp -d)"; iconset="$$work/AppIcon.iconset"; mkdir -p "$$iconset"; \
		for sz in 16 32 128 256 512; do \
			sips -z $$sz         $$sz         "$(ICON_PNG)" --out "$$iconset/icon_$${sz}x$${sz}.png" >/dev/null; \
			sips -z $$((sz*2))   $$((sz*2))   "$(ICON_PNG)" --out "$$iconset/icon_$${sz}x$${sz}@2x.png" >/dev/null; \
		done; \
		iconutil -c icns "$$iconset" -o "$(APP_BUNDLE)/Contents/Resources/AppIcon.icns"; \
		rm -rf "$$work"; \
		echo "make app: generated Contents/Resources/AppIcon.icns from $(ICON_PNG) (sips)"; \
	else \
		echo "make app: iconutil/sips/rsvg-convert not found — skipping .icns (not a blocker)"; \
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
	rm -f $(SYSO_AMD64) $(SYSO_ARM64)
