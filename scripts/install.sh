#!/usr/bin/env bash
# Installs hyp-vpn on macOS or Linux: openconnect, the tray binary, the
# root-owned privileged helper, and a sudoers rule scoped to that helper.
# Idempotent: safe to re-run; updates everything in place.
#
# Usage:
#   bash scripts/install.sh                            # build from this checkout
#   HYP_VPN_RELEASE_URL=<url> bash scripts/install.sh  # install a prebuilt binary
#   HYP_VPN_OPENCONNECT=/path/to/openconnect bash scripts/install.sh
#
# THREAT MODEL (the same one documented in scripts/hyp-vpn-tunnel):
#
#   The sudoers rule written here grants the invoking user passwordless root for
#   one script. That script validates its arguments, so argument injection into
#   openconnect (--script=, --csd-wrapper=, both of which openconnect runs as
#   root) is blocked on both platforms.
#
#   What is NOT uniformly enforceable is binary tampering. The helper executes an
#   absolute openconnect path baked in below, and this installer checks that the
#   binary and every directory above it are root-owned and not group/other
#   writable. On Linux a failure aborts the install. On macOS it prints a warning
#   and continues, because Homebrew owns its prefix as the installing user by
#   design: whoever can write there could swap openconnect and get root through
#   the sudoers rule. On a single-user mac that person is already an admin who can
#   run sudo directly, so the rule grants them nothing they did not have. On a
#   shared mac, install openconnect somewhere root-owned and pass
#   HYP_VPN_OPENCONNECT. This is a documented boundary, not a claim of safety.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RELEASE_URL="${HYP_VPN_RELEASE_URL:-}"
BIN_TARGET=/usr/local/bin/hyp-vpn
HELPER_SRC="$REPO_DIR/scripts/hyp-vpn-tunnel"
# Overridable so a machine whose /usr/local is user-owned (an Intel-mac Homebrew
# prefix) can put the helper somewhere root-owned instead of loosening the check.
# Changing it means matching "helper_path" in the app's config.json.
HELPER_DIR="${HYP_VPN_HELPER_DIR:-/usr/local/libexec}"
HELPER_TARGET="$HELPER_DIR/hyp-vpn-tunnel"
SUDOERS_TARGET=/etc/sudoers.d/hyp-vpn
OS="$(uname -s)"

log() { printf 'install: %s\n' "$1"; }
warn() { printf 'install: WARNING: %s\n' "$1" >&2; }
die() {
	printf 'install: error: %s\n' "$1" >&2
	exit 1
}

case "$OS" in
Darwin)
	ROOT_GROUP=wheel
	;;
Linux)
	ROOT_GROUP=root
	;;
*) die "unsupported OS: $OS" ;;
esac

# stat_field prints one attribute of a path. BSD and GNU stat disagree on flags,
# so the difference is confined here. $1 is owner|mode. Both implementations
# default to lstat (they do not follow symlinks), which is what the walk below
# wants: a group-writable symlink is a hazard in its own right, and its target is
# checked separately.
stat_field() {
	if [[ "$OS" == Darwin ]]; then
		case "$1" in
		owner) stat -f '%u' "$2" ;;
		mode) stat -f '%Lp' "$2" ;;
		esac
	else
		case "$1" in
		owner) stat -c '%u' "$2" ;;
		mode) stat -c '%a' "$2" ;;
		esac
	fi
}

# path_unsafe prints why $1 cannot be trusted on the path to something executed as
# root, or nothing if it is fine. A non-root owner or a group/other write bit both
# mean somebody other than root can replace what sits there.
path_unsafe() {
	local p="$1" owner mode
	owner="$(stat_field owner "$p" 2>/dev/null)" || {
		echo "cannot stat $p"
		return 0
	}
	if [[ "$owner" != 0 ]]; then
		echo "$p is owned by uid $owner, not root"
		return 0
	fi
	mode="$(stat_field mode "$p" 2>/dev/null)" || {
		echo "cannot stat $p"
		return 0
	}
	if ((8#$mode & 8#22)); then
		echo "$p is group- or other-writable (mode $mode)"
	fi
}

# walk_chain prints a problem line for $1 and for every directory above it.
# Non-existent leaves are skipped: this installer creates them root-owned, and
# their ancestors are checked regardless.
walk_chain() {
	local p="$1"
	# Skip the components that do not exist yet: this installer creates them
	# root-owned and 0755, so start the walk at the first existing ancestor.
	while [[ ! -e "$p" && ! -L "$p" && "$p" != / ]]; do p="$(dirname "$p")"; done
	while :; do
		path_unsafe "$p"
		[[ "$p" == / ]] && break
		p="$(dirname "$p")"
	done
}

# check_chain reports whether $1 is safe to reach from a passwordless-root
# context. $2 is "abort" or "warn"; returns non-zero when anything was flagged.
#
# It walks the path twice: once literally, so a group-writable *symlink* anywhere
# in it is caught (stat does not follow links), and once fully resolved, so a
# root-owned symlink pointing into a user-writable directory is caught too.
# Checking only one of the two leaves an obvious hole.
check_chain() {
	local target="$1" enforcement="$2" resolved problems
	resolved="$(readlink -f "$target" 2>/dev/null || echo "$target")"
	problems="$(walk_chain "$target")"
	if [[ "$resolved" != "$target" ]]; then
		problems="$problems"$'\n'"$(walk_chain "$resolved")"
	fi
	problems="$(printf '%s\n' "$problems" | grep -v '^$' | sort -u || true)"
	[[ -n "$problems" ]] || return 0
	if [[ "$enforcement" == abort ]]; then
		printf 'install: error: %s\n' "$problems" >&2
		printf 'install: error: %s\n' \
			"anything reachable from a passwordless-root path must be root-owned and not writable by others." \
			"Remedy: sudo chown root:$ROOT_GROUP $HELPER_DIR && sudo chmod 755 $HELPER_DIR" \
			"(an Intel-mac Homebrew prefix leaves /usr/local user-owned), or point the helper" \
			"somewhere already root-owned by setting HYP_VPN_HELPER_DIR=/usr/libexec and matching" \
			"\"helper_path\" in ~/Library/Application Support/hyp-vpn/config.json." >&2
		exit 1
	fi
	while IFS= read -r problem; do warn "$problem"; done <<<"$problems"
	return 1
}

# preflight_paths enforces the helper's path and merely warns about the tray
# binary's, because the two carry different consequences.
#
# The helper is what the sudoers rule names, so a writable path to it is a direct
# passwordless-root hole: fatal, with a remedy in the message. The tray binary runs
# as the user who already holds that rule, so replacing it escalates nothing they
# could not already do by calling the helper themselves — a warning is the honest
# severity, and hard-aborting there would block installs on Intel-mac Homebrew
# layouts (user-owned /usr/local) for no security gain.
preflight_paths() {
	check_chain "$HELPER_TARGET" abort || true
	if ! check_chain "$BIN_TARGET" warn; then
		warn "$BIN_TARGET sits on a path others can write; they could replace the tray app."
		warn "Not fatal (it runs unprivileged, as the user who already holds the sudoers rule),"
		warn "but on a shared machine install it somewhere root-owned."
	fi
	log "checked the paths to $HELPER_TARGET and $BIN_TARGET"
}

# resolve_principal decides which user the sudoers rule names. Running the whole
# installer under sudo is the common mistake: $USER would then be root and the
# rule would grant nothing to the human who has to use it.
resolve_principal() {
	local user
	if [[ "$(id -u)" -eq 0 ]]; then
		[[ -n "${SUDO_USER:-}" ]] ||
			die "run this as your normal user, not root: it uses sudo where it needs to"
		user="$SUDO_USER"
	else
		user="$(id -un)"
	fi
	[[ "$user" =~ ^[A-Za-z0-9._-]+$ ]] ||
		die "refusing to write a sudoers rule for user name '$user': unexpected characters"
	[[ "$user" != root ]] || die "refusing to write a sudoers rule for root"
	PRINCIPAL="$user"
}

install_openconnect() {
	if command -v openconnect >/dev/null 2>&1; then
		log "openconnect already installed ($(command -v openconnect))"
		return
	fi
	case "$OS" in
	Darwin)
		command -v brew >/dev/null 2>&1 || die "Homebrew required: https://brew.sh"
		brew install openconnect
		;;
	Linux)
		if command -v apt-get >/dev/null 2>&1; then sudo apt-get install -y openconnect
		elif command -v dnf >/dev/null 2>&1; then sudo dnf install -y openconnect
		elif command -v pacman >/dev/null 2>&1; then sudo pacman -S --noconfirm openconnect
		else die "no supported package manager found; install openconnect manually"
		fi
		;;
	esac
}

# resolve_openconnect picks the absolute openconnect path to bake into the helper
# and verifies nothing writable sits on the way to it.
#
# The path is deliberately NOT resolved through symlinks: on macOS `command -v`
# yields /opt/homebrew/bin/openconnect, a stable symlink, whereas its target is
# version-pinned (.../Cellar/openconnect/9.21/bin/openconnect) and disappears on
# the next `brew upgrade`, which would leave the helper pointing at nothing.
# check_chain examines both the symlink and its target, so keeping the stable path
# costs no coverage.
resolve_openconnect() {
	local p
	p="${HYP_VPN_OPENCONNECT:-$(command -v openconnect || true)}"
	[[ -n "$p" ]] || die "openconnect not found after install"
	[[ -x "$p" ]] || die "$p is not executable"
	[[ "$p" == /* ]] || die "openconnect path must be absolute, got '$p'"
	# The path is written into a single-quoted shell assignment in the helper, and
	# the helper runs as root: refuse anything that could break out of it.
	[[ "$p" =~ ^[A-Za-z0-9._/+-]+$ ]] ||
		die "openconnect path '$p' contains characters unsafe to embed in the helper"

	if [[ "$OS" == Linux ]]; then
		check_chain "$p" abort || true
	elif ! check_chain "$p" warn; then
		warn "openconnect at $p is not on a root-owned path (normal for Homebrew)."
		warn "Anyone who can write there gains root via the hyp-vpn sudoers rule."
		warn "On a shared mac, install openconnect root-owned and re-run with HYP_VPN_OPENCONNECT=<path>."
	fi
	OPENCONNECT_PATH="$p"
	log "openconnect resolved to $OPENCONNECT_PATH"
}

install_binary() {
	if [[ -n "$RELEASE_URL" ]]; then
		local tmp
		tmp="$(mktemp)"
		curl -fsSL "$RELEASE_URL" -o "$tmp"
		sudo install -o root -m 0755 "$tmp" "$BIN_TARGET"
		rm -f "$tmp"
		log "installed $BIN_TARGET from $RELEASE_URL"
	else
		(cd "$REPO_DIR" && make build)
		sudo install -o root -m 0755 "$REPO_DIR/hyp-vpn" "$BIN_TARGET"
		log "built from checkout and installed $BIN_TARGET"
	fi
}

# install_helper bakes the verified openconnect path into the installed copy. The
# repo copy keeps the @OPENCONNECT@ placeholder and refuses to run, so a helper
# that skipped this step cannot start a tunnel.
install_helper() {
	local tmp subs
	tmp="$(mktemp)"
	subs=0
	while IFS= read -r line; do
		if [[ "$line" == "OPENCONNECT='@OPENCONNECT@'" ]]; then
			printf "OPENCONNECT='%s'\n" "$OPENCONNECT_PATH"
			subs=$((subs + 1))
		else
			printf '%s\n' "$line"
		fi
	done <"$HELPER_SRC" >"$tmp"
	if [[ "$subs" -ne 1 ]]; then
		rm -f "$tmp"
		die "expected exactly one @OPENCONNECT@ placeholder in $HELPER_SRC, found $subs"
	fi
	sh -n "$tmp" || {
		rm -f "$tmp"
		die "generated helper is not valid shell"
	}
	sudo install -d -o root -m 0755 "$HELPER_DIR"
	sudo install -o root -m 0755 "$tmp" "$HELPER_TARGET"
	rm -f "$tmp"
	log "installed $HELPER_TARGET (root, 0755, openconnect=$OPENCONNECT_PATH)"
}

install_sudoers() {
	local rule tmp
	# No arguments are listed, so any argv is permitted — the helper validates its
	# own. What matters is that the path is the helper and never openconnect: a
	# NOPASSWD rule on openconnect is passwordless root by way of --script=.
	rule="$PRINCIPAL ALL=(root) NOPASSWD: $HELPER_TARGET"
	tmp="$(mktemp)"
	printf '%s\n' "$rule" >"$tmp"
	chmod 0440 "$tmp"
	# Validate before activating: a syntactically broken file in /etc/sudoers.d
	# can lock everyone out of sudo.
	if ! sudo visudo -c -f "$tmp" >/dev/null; then
		rm -f "$tmp"
		die "sudoers validation failed; nothing installed"
	fi
	sudo install -o root -m 0440 "$tmp" "$SUDOERS_TARGET"
	rm -f "$tmp"
	log "installed $SUDOERS_TARGET: $rule"
}

verify() {
	# "stop" with no tunnel running is a successful no-op, which makes it the
	# cheapest end-to-end check that the rule, the path and the mode all line up.
	sudo -n "$HELPER_TARGET" stop >/dev/null 2>&1 ||
		die "'sudo -n $HELPER_TARGET stop' still prompts or fails; the sudoers rule is not effective for $PRINCIPAL"
	log "verified passwordless helper invocation as $PRINCIPAL"
}

resolve_principal
preflight_paths
install_openconnect
resolve_openconnect
install_binary
install_helper
install_sudoers
verify

log "done. Launch the app with: $BIN_TARGET &"
log "First connect opens a browser window for the SAML login."
log "Quit FortiClient before connecting — two clients must not share the tunnel."
