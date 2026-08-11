BIN  := hyp-vpn
DIST := dist
PKG  := ./cmd/hyp-vpn

.PHONY: all build test release clean install

all: build

build:
	go build -o $(BIN) $(PKG)

test:
	go vet ./...
	go test -race ./...

# Cross-compilation notes:
#  - darwin needs cgo: fyne.io/systray drives the Cocoa status bar. The amd64
#    slice cross-builds from an Apple Silicon mac because the macOS SDK is a
#    fat SDK; if a future SDK drops x86_64 support, drop that line (the team is
#    all Apple Silicon).
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

# Installs on this machine (macOS/Linux): openconnect, binary, privileged
# helper, sudoers rule. Prompts for sudo.
install:
	bash scripts/install.sh

clean:
	rm -rf $(DIST) $(BIN)
