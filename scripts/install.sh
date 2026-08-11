#!/usr/bin/env bash
# Installs Postern on macOS or Linux: openconnect, the tray binary, the
# root-owned privileged helper, and a sudoers rule scoped to that helper.
# Idempotent: safe to re-run; updates everything in place.
#
# Usage:
#   POSTERN_GATEWAY=vpn.example.com:10443 bash scripts/install.sh
#
# POSTERN_GATEWAY is required on a first install: the app ships with no gateway
# (it is deployment-specific) and the installer writes the one you give here into
# your own config.json. Re-runs on a machine whose config already names a gateway
# do not need it.
#
# Other knobs:
#   POSTERN_RELEASE_URL=<url>                 # install a prebuilt binary instead of building
#   POSTERN_OPENCONNECT=/path/to/openconnect  # use this openconnect, not the one on PATH
#   POSTERN_HELPER_DIR=/usr/libexec           # install the privileged helper elsewhere
#
# THREAT MODEL (the same one documented in scripts/postern-tunnel):
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
#   POSTERN_OPENCONNECT. This is a documented boundary, not a claim of safety.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RELEASE_URL="${POSTERN_RELEASE_URL:-}"
BIN_TARGET=/usr/local/bin/postern
HELPER_SRC="$REPO_DIR/scripts/postern-tunnel"
# Overridable so a machine whose /usr/local is user-owned (an Intel-mac Homebrew
# prefix) can put the helper somewhere root-owned instead of loosening the check.
# Changing it means matching "helper_path" in the app's config.json.
HELPER_DIR="${POSTERN_HELPER_DIR:-/usr/local/libexec}"
HELPER_TARGET="$HELPER_DIR/postern-tunnel"
SUDOERS_TARGET=/etc/sudoers.d/postern
GATEWAY="${POSTERN_GATEWAY:-}"
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
			"somewhere already root-owned by setting POSTERN_HELPER_DIR=/usr/libexec and matching" \
			"\"helper_path\" in ~/Library/Application Support/postern/config.json." >&2
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

# validate_gateway applies exactly the charset and shape rule that
# scripts/postern-tunnel enforces on the argument it hands to openconnect. Keeping
# them identical means a gateway this installer accepts is one the helper will
# accept too — otherwise the install "succeeds" and every connect dies in the
# privileged helper.
validate_gateway() {
	local gw="$1" host port
	case "$gw" in
	-*) die "POSTERN_GATEWAY must not start with '-': '$gw'" ;;
	*[!A-Za-z0-9.:_-]*) die "POSTERN_GATEWAY contains invalid characters: '$gw'" ;;
	*:*) ;;
	*) die "POSTERN_GATEWAY must be host:port, got '$gw'" ;;
	esac
	host="${gw%:*}"
	port="${gw##*:}"
	case "$host" in
	'' | *:*) die "POSTERN_GATEWAY must be host:port, got '$gw'" ;;
	esac
	case "$port" in
	'' | *[!0-9]*) die "POSTERN_GATEWAY port must be numeric, got '$gw'" ;;
	esac
	GATEWAY_HOST="$host"
	GATEWAY_PORT="$port"
}

# principal_home prints $PRINCIPAL's home directory. Under sudo, $HOME is root's,
# so the config would land where the app never looks. PRINCIPAL has already been
# checked against ^[A-Za-z0-9._-]+$ by resolve_principal, which is what makes the
# tilde expansion below safe to eval.
principal_home() {
	local h=""
	eval "h=~$PRINCIPAL"
	[[ -n "$h" && -d "$h" ]] || die "cannot find the home directory of $PRINCIPAL"
	printf '%s\n' "$h"
}

# as_principal runs "$@" as $PRINCIPAL. Running the whole installer under sudo is a
# common mistake, and nothing it creates in the user's home may end up root-owned:
# ~/.config often does not exist yet, so even the directory matters. We are already
# root on that branch, so sudo does not prompt.
as_principal() {
	if [[ "$(id -u)" -eq 0 ]]; then
		sudo -u "$PRINCIPAL" "$@"
	else
		"$@"
	fi
}

# config_dir mirrors config.DefaultDir() in the Go app — os.UserConfigDir() plus
# "postern". Keep the two in step: a mismatch means this installer writes a config
# the app silently ignores, and the tray reports "gateway not set" after a
# successful install.
#
# XDG_CONFIG_HOME is honoured only when we are not root, because sudo would have
# handed us root's copy of it rather than the user's.
config_dir() {
	local home
	home="$(principal_home)"
	if [[ "$OS" == Darwin ]]; then
		printf '%s\n' "$home/Library/Application Support/postern"
	elif [[ "$(id -u)" -ne 0 && -n "${XDG_CONFIG_HOME:-}" ]]; then
		printf '%s\n' "$XDG_CONFIG_HOME/postern"
	else
		printf '%s\n' "$home/.config/postern"
	fi
}

# config_has_gateway reports whether $1 already sets a non-empty "gateway". A grep
# rather than jq: jq is not installed by default anywhere this script runs, and
# the question is narrow enough not to need a JSON parser.
config_has_gateway() {
	grep -qE '"gateway"[[:space:]]*:[[:space:]]*"[^"]+"' "$1" 2>/dev/null
}

# install_config makes sure the app has a gateway to dial before anything
# privileged happens, so a missing POSTERN_GATEWAY costs the user nothing but a
# re-run.
#
# An existing config.json is never rewritten: it holds the user's own settings
# (helper_path, autostart, port) and this installer has no merge logic. When one
# exists without a gateway, the fix is spelled out instead.
install_config() {
	local dir file
	dir="$(config_dir)"
	file="$dir/config.json"

	if [[ -e "$file" ]]; then
		if config_has_gateway "$file"; then
			[[ -z "$GATEWAY" ]] ||
				warn "$file already sets a gateway; POSTERN_GATEWAY ignored (edit the file to change it)"
			log "using the gateway already configured in $file"
			return
		fi
		die "$file exists but sets no gateway. Add one by hand (this installer will not rewrite an existing config): \"gateway\": \"<host>\", \"port\": <port>"
	fi

	[[ -n "$GATEWAY" ]] ||
		die "POSTERN_GATEWAY is required: re-run as POSTERN_GATEWAY=vpn.example.com:10443 bash scripts/install.sh (it is written to $file)"
	validate_gateway "$GATEWAY"

	as_principal mkdir -p "$dir"
	printf '{\n  "gateway": "%s",\n  "port": %s\n}\n' "$GATEWAY_HOST" "$GATEWAY_PORT" |
		as_principal tee "$file" >/dev/null
	as_principal chmod 0700 "$dir"
	as_principal chmod 0600 "$file"
	log "wrote $file (gateway $GATEWAY_HOST:$GATEWAY_PORT)"
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
	p="${POSTERN_OPENCONNECT:-$(command -v openconnect || true)}"
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
		warn "Anyone who can write there gains root via the postern sudoers rule."
		warn "On a shared mac, install openconnect root-owned and re-run with POSTERN_OPENCONNECT=<path>."
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
		sudo install -o root -m 0755 "$REPO_DIR/postern" "$BIN_TARGET"
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
# Before anything privileged: a missing or malformed POSTERN_GATEWAY should cost a
# re-run, not a half-finished install.
install_config
preflight_paths
install_openconnect
resolve_openconnect
install_binary
install_helper
install_sudoers
verify

log "done. Launch the app with: $BIN_TARGET &"
log "Gateway lives in $(config_dir)/config.json; edit it there to point elsewhere."
log "First connect opens a browser window for the SAML login."
log "Quit FortiClient before connecting — two clients must not share the tunnel."
