#!/usr/bin/env bash
# Installs hyp-vpn on macOS or Linux: openconnect, the tray binary, the
# root-owned privileged helper, and a sudoers rule scoped to that helper.
# Idempotent: safe to re-run; updates everything in place.
#
# Usage:
#   bash scripts/install.sh                          # build from this checkout
#   HYP_VPN_RELEASE_URL=<url> bash scripts/install.sh  # install a prebuilt binary
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RELEASE_URL="${HYP_VPN_RELEASE_URL:-}"
BIN_TARGET=/usr/local/bin/hyp-vpn
HELPER_SRC="$REPO_DIR/scripts/hyp-vpn-tunnel"
HELPER_TARGET=/usr/local/libexec/hyp-vpn-tunnel
SUDOERS_TARGET=/etc/sudoers.d/hyp-vpn
OS="$(uname -s)"

log() { printf 'install: %s\n' "$1"; }

install_openconnect() {
	if command -v openconnect >/dev/null 2>&1; then
		log "openconnect already installed ($(command -v openconnect))"
		return
	fi
	case "$OS" in
	Darwin)
		command -v brew >/dev/null 2>&1 || { echo "Homebrew required: https://brew.sh" >&2; exit 1; }
		brew install openconnect
		;;
	Linux)
		if command -v apt-get >/dev/null 2>&1; then sudo apt-get install -y openconnect
		elif command -v dnf >/dev/null 2>&1; then sudo dnf install -y openconnect
		elif command -v pacman >/dev/null 2>&1; then sudo pacman -S --noconfirm openconnect
		else echo "no supported package manager found; install openconnect manually" >&2; exit 1
		fi
		;;
	*) echo "unsupported OS: $OS" >&2; exit 1 ;;
	esac
}

install_binary() {
	if [[ -n "$RELEASE_URL" ]]; then
		local tmp
		tmp="$(mktemp)"
		curl -fsSL "$RELEASE_URL" -o "$tmp"
		sudo install -m 0755 "$tmp" "$BIN_TARGET"
		rm -f "$tmp"
		log "installed $BIN_TARGET from $RELEASE_URL"
	else
		(cd "$REPO_DIR" && make build)
		sudo install -m 0755 "$REPO_DIR/hyp-vpn" "$BIN_TARGET"
		log "built from checkout and installed $BIN_TARGET"
	fi
}

install_helper() {
	# Root-owned helper in a root-owned directory: the sudoers rule below hands
	# out passwordless root for exactly this path, so no user-writable byte may
	# be reachable from it.
	sudo install -d -o root -m 0755 /usr/local/libexec
	sudo install -o root -m 0755 "$HELPER_SRC" "$HELPER_TARGET"
	log "installed $HELPER_TARGET (root, 0755)"
}

install_sudoers() {
	local rule tmp
	rule="$USER ALL=(root) NOPASSWD: $HELPER_TARGET"
	tmp="$(mktemp)"
	printf '%s\n' "$rule" >"$tmp"
	chmod 0440 "$tmp"
	# Validate before activating: a syntactically broken file in /etc/sudoers.d
	# can lock everyone out of sudo.
	if ! sudo visudo -c -f "$tmp" >/dev/null; then
		rm -f "$tmp"
		echo "sudoers validation failed; not installed" >&2
		exit 1
	fi
	sudo install -o root -g "$(id -g root 2>/dev/null || echo 0)" -m 0440 "$tmp" "$SUDOERS_TARGET"
	rm -f "$tmp"
	log "installed $SUDOERS_TARGET: $rule"
}

verify() {
	sudo -n "$HELPER_TARGET" stop >/dev/null 2>&1 \
		|| { echo "verification failed: 'sudo -n $HELPER_TARGET stop' still prompts or errors" >&2; exit 1; }
	log "verified passwordless helper invocation"
}

install_openconnect
install_binary
install_helper
install_sudoers
verify

log "done. Launch the app with: $BIN_TARGET &"
log "First connect opens a browser window for the SAML login."
log "Quit FortiClient before connecting — two clients must not share the tunnel."
