#!/usr/bin/env bash
# Installs OpenFortiTray on macOS or Linux: openconnect, the tray binary, the
# root-owned privileged helper, and a sudoers rule scoped to that helper.
# Idempotent: safe to re-run; updates everything in place.
#
# Usage:
#   OPENFORTITRAY_GATEWAY=vpn.example.com:10443 bash scripts/install.sh
#
# OPENFORTITRAY_GATEWAY is required on a first install: the app ships with no gateway
# (it is deployment-specific) and the installer writes the one you give here into
# your own config.json. Re-runs on a machine whose config already names a gateway
# do not need it.
#
# Other knobs:
#   OPENFORTITRAY_RELEASE_URL=<url>                 # install a prebuilt binary instead of building
#   OPENFORTITRAY_OPENCONNECT=/path/to/openconnect  # use this openconnect, not the one on PATH
#   OPENFORTITRAY_HELPER_DIR=/usr/libexec           # install the privileged helper elsewhere
#
# OPENFORTITRAY_HELPER_DIR moves three things at once: the helper, the sudoers rule that
# names it, and the "helper_path" the app dials. On a first install this script
# records it in config.json for you. On a machine that already has a config.json it
# cannot rewrite one, so a mismatch between the two is refused with instructions
# rather than installed — a helper the app never calls, or one it calls without a
# sudoers rule, fails at every connect with a password prompt the tray cannot answer.
#
# THREAT MODEL (the same one documented in scripts/openfortitray-tunnel):
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
#   OPENFORTITRAY_OPENCONNECT. This is a documented boundary, not a claim of safety.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RELEASE_URL="${OPENFORTITRAY_RELEASE_URL:-}"
BIN_TARGET=/usr/local/bin/openfortitray
HELPER_SRC="$REPO_DIR/scripts/openfortitray-tunnel"
# Overridable so a machine whose /usr/local is user-owned (an Intel-mac Homebrew
# prefix) can put the helper somewhere root-owned instead of loosening the check.
# Keep DEFAULT_HELPER_DIR in step with tunnel.DefaultHelperPath in the Go app: it is
# what the app dials when config.json sets no "helper_path", so this script has to
# know it to tell "the user chose this path" from "the app's built-in default".
DEFAULT_HELPER_DIR=/usr/local/libexec
DEFAULT_HELPER_TARGET="$DEFAULT_HELPER_DIR/openfortitray-tunnel"
HELPER_DIR="${OPENFORTITRAY_HELPER_DIR:-$DEFAULT_HELPER_DIR}"
HELPER_TARGET="$HELPER_DIR/openfortitray-tunnel"
SUDOERS_TARGET=/etc/sudoers.d/openfortitray
GATEWAY="${OPENFORTITRAY_GATEWAY:-}"
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

# On macOS the tray ships as a hand-rolled .app bundle so LSUIElement takes
# effect (menu-bar only, no Dock icon, reliable status item); on Linux it stays a
# bare binary. TRAY_TARGET is the executable this install ends up placing the
# tray at — used by the writable-path preflight and the closing hint — and the
# LaunchAgent (internal/autostart) points at the bundle executable to match.
APP_TARGET=/Applications/OpenFortiTray.app
APP_EXEC="$APP_TARGET/Contents/MacOS/openfortitray"
if [[ "$OS" == Darwin ]]; then
	TRAY_TARGET="$APP_EXEC"
else
	TRAY_TARGET="$BIN_TARGET"
fi

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
			"somewhere already root-owned by setting OPENFORTITRAY_HELPER_DIR=/usr/libexec — this script" \
			"records that as \"helper_path\" when it writes config.json, and refuses to run at all" \
			"if an existing config.json names a different one." >&2
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
	if ! check_chain "$TRAY_TARGET" warn; then
		warn "$TRAY_TARGET sits on a path others can write; they could replace the tray app."
		warn "Not fatal (it runs unprivileged, as the user who already holds the sudoers rule),"
		warn "but on a shared machine install it somewhere root-owned."
	fi
	log "checked the paths to $HELPER_TARGET and $TRAY_TARGET"
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
# scripts/openfortitray-tunnel enforces on the argument it hands to openconnect. Keeping
# them identical means a gateway this installer accepts is one the helper will
# accept too — otherwise the install "succeeds" and every connect dies in the
# privileged helper.
validate_gateway() {
	local gw="$1" host port
	case "$gw" in
	-*) die "OPENFORTITRAY_GATEWAY must not start with '-': '$gw'" ;;
	*[!A-Za-z0-9.:_-]*) die "OPENFORTITRAY_GATEWAY contains invalid characters: '$gw'" ;;
	*:*) ;;
	*) die "OPENFORTITRAY_GATEWAY must be host:port, got '$gw'" ;;
	esac
	host="${gw%:*}"
	port="${gw##*:}"
	case "$host" in
	'' | *:*) die "OPENFORTITRAY_GATEWAY must be host:port, got '$gw'" ;;
	esac
	case "$port" in
	'' | *[!0-9]*) die "OPENFORTITRAY_GATEWAY port must be numeric, got '$gw'" ;;
	esac
	GATEWAY_HOST="$host"
	GATEWAY_PORT="$port"
}

# validate_helper_dir refuses a OPENFORTITRAY_HELPER_DIR that cannot be embedded safely.
# The path ends up in three places with three different quoting rules — a sudoers
# rule, a JSON string in config.json, and a sudo command line — so rather than escape
# for each, anything outside a conservative charset is rejected. A trailing slash is
# refused too: it would make the installed path ("$dir//openfortitray-tunnel") differ as a
# string from the one written to config.json, and sudoers matches on the string.
validate_helper_dir() {
	[[ "$HELPER_DIR" == /* ]] ||
		die "OPENFORTITRAY_HELPER_DIR must be an absolute path, got '$HELPER_DIR'"
	[[ "$HELPER_DIR" != */ ]] ||
		die "OPENFORTITRAY_HELPER_DIR must not end in '/', got '$HELPER_DIR'"
	[[ "$HELPER_DIR" =~ ^[A-Za-z0-9._/+-]+$ ]] ||
		die "OPENFORTITRAY_HELPER_DIR contains characters unsafe to embed in a sudoers rule and in config.json: '$HELPER_DIR'"
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
# "openfortitray". Keep the two in step: a mismatch means this installer writes a config
# the app silently ignores, and the tray reports "gateway not set" after a
# successful install.
#
# XDG_CONFIG_HOME is honoured only when we are not root, because sudo would have
# handed us root's copy of it rather than the user's.
config_dir() {
	local home
	home="$(principal_home)"
	if [[ "$OS" == Darwin ]]; then
		printf '%s\n' "$home/Library/Application Support/openfortitray"
	elif [[ "$(id -u)" -ne 0 && -n "${XDG_CONFIG_HOME:-}" ]]; then
		printf '%s\n' "$XDG_CONFIG_HOME/openfortitray"
	else
		printf '%s\n' "$home/.config/openfortitray"
	fi
}

# config_has_gateway reports whether $1 already sets a non-empty "gateway". A grep
# rather than jq: jq is not installed by default anywhere this script runs, and
# the question is narrow enough not to need a JSON parser.
config_has_gateway() {
	grep -qE '"gateway"[[:space:]]*:[[:space:]]*"[^"]+"' "$1" 2>/dev/null
}

# config_helper_path prints the "helper_path" string set in $1, or nothing when the
# key is absent or the file does not exist.
#
# DELIBERATELY FRAGILE: a sed expression, not a JSON parser, for the same reason as
# config_has_gateway — jq is not installed by default on any platform this script
# runs on, and pulling in a dependency to read one string is a worse trade. It
# understands the one-key-per-line shape this script writes and a human edits, and
# nothing else: no escape sequences, no two keys on one line, no comments. Anything
# it cannot read looks to it like a config with no helper_path, i.e. the default —
# which is why verify() re-checks the path it derived by actually invoking it
# instead of trusting this.
config_helper_path() {
	sed -n 's/.*"helper_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$1" 2>/dev/null |
		head -n 1
}

# effective_helper_path prints the helper the *app* will invoke: whatever
# config.json names, or the app's built-in default when it names nothing. This, not
# HELPER_TARGET, is the path that has to be reachable through sudo -n; the two agree
# on a normal install and can disagree on a re-run with a different
# OPENFORTITRAY_HELPER_DIR, which is precisely the case this exists to catch.
effective_helper_path() {
	local configured
	configured="$(config_helper_path "$(config_dir)/config.json")"
	printf '%s\n' "${configured:-$DEFAULT_HELPER_TARGET}"
}

# require_config_helper_match aborts when the app would dial a different helper from
# the one this run is about to install. It runs before anything privileged happens,
# because the alternative is the trap this replaces: the installer places the helper
# and the sudoers rule at HELPER_TARGET, verifies *that* path, reports success — and
# every connect then fails, because the app dials the path in config.json, for which
# no sudoers rule exists.
#
# It cannot pick a side on the user's behalf: this script does not rewrite an
# existing config, and either path may be the intended one. So it names both and the
# two ways to reconcile them.
require_config_helper_match() {
	local file="$1" configured effective
	configured="$(config_helper_path "$file")"
	effective="${configured:-$DEFAULT_HELPER_TARGET}"
	[[ "$effective" != "$HELPER_TARGET" ]] || return 0

	printf 'install: error: %s\n' \
		"the app and this install disagree about where the privileged helper lives." >&2
	if [[ -n "$configured" ]]; then
		printf 'install: error: %s\n' "  $file sets \"helper_path\": \"$configured\"" >&2
	else
		printf 'install: error: %s\n' \
			"  $file sets no \"helper_path\", so the app uses the default $DEFAULT_HELPER_TARGET" >&2
	fi
	printf 'install: error: %s\n' \
		"  this run would install the helper and the sudoers rule at $HELPER_TARGET" \
		"Nothing has been installed. Reconcile them one of two ways:" \
		"  1. re-run with OPENFORTITRAY_HELPER_DIR=$(dirname "$effective")   (install where the app already looks)" \
		"  2. set \"helper_path\": \"$HELPER_TARGET\" in $file   (point the app at this run's location)" >&2
	exit 1
}

# install_config makes sure the app has a gateway to dial before anything
# privileged happens, so a missing OPENFORTITRAY_GATEWAY costs the user nothing but a
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
				warn "$file already sets a gateway; OPENFORTITRAY_GATEWAY ignored (edit the file to change it)"
			log "using the gateway already configured in $file"
			# Checked here rather than at the end: an existing config that names a
			# different helper is a stop-before-you-touch-anything condition.
			require_config_helper_match "$file"
			return
		fi
		die "$file exists but sets no gateway. Add one by hand (this installer will not rewrite an existing config): \"gateway\": \"<host>\", \"port\": <port>"
	fi

	[[ -n "$GATEWAY" ]] ||
		die "OPENFORTITRAY_GATEWAY is required: re-run as OPENFORTITRAY_GATEWAY=vpn.example.com:10443 bash scripts/install.sh (it is written to $file)"
	validate_gateway "$GATEWAY"

	as_principal mkdir -p "$dir"
	# helper_path is written only when it differs from the app's built-in default,
	# so an ordinary install still produces a two-key config with nothing in it to
	# go stale. When OPENFORTITRAY_HELPER_DIR *is* in play the key is mandatory: without
	# it the app would dial $DEFAULT_HELPER_TARGET while the helper and its sudoers
	# rule sit somewhere else — an install that verifies clean and never connects.
	# (validate_helper_dir has already refused any path needing JSON escaping.)
	if [[ "$HELPER_TARGET" == "$DEFAULT_HELPER_TARGET" ]]; then
		printf '{\n  "gateway": "%s",\n  "port": %s\n}\n' "$GATEWAY_HOST" "$GATEWAY_PORT"
	else
		printf '{\n  "gateway": "%s",\n  "port": %s,\n  "helper_path": "%s"\n}\n' \
			"$GATEWAY_HOST" "$GATEWAY_PORT" "$HELPER_TARGET"
	fi | as_principal tee "$file" >/dev/null
	as_principal chmod 0700 "$dir"
	as_principal chmod 0600 "$file"
	if [[ "$HELPER_TARGET" == "$DEFAULT_HELPER_TARGET" ]]; then
		log "wrote $file (gateway $GATEWAY_HOST:$GATEWAY_PORT)"
	else
		log "wrote $file (gateway $GATEWAY_HOST:$GATEWAY_PORT, helper_path $HELPER_TARGET)"
	fi
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
	p="${OPENFORTITRAY_OPENCONNECT:-$(command -v openconnect || true)}"
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
		warn "Anyone who can write there gains root via the openfortitray sudoers rule."
		warn "On a shared mac, install openconnect root-owned and re-run with OPENFORTITRAY_OPENCONNECT=<path>."
	fi
	OPENCONNECT_PATH="$p"
	log "openconnect resolved to $OPENCONNECT_PATH"
}

# install_app_bundle builds the macOS .app (make app) and installs it to
# /Applications so LSUIElement=1 is honoured — a bare /usr/local/bin binary would
# render the fyne status item unreliably and show a Dock icon. The LaunchAgent
# points its ProgramArguments at "$APP_EXEC", so login-launch reads the same
# Info.plist. Prebuilt bundle downloads are deferred to the Fyne 5 packaging work.
install_app_bundle() {
	local src="$REPO_DIR/dist/OpenFortiTray.app"
	if [[ -n "$RELEASE_URL" ]]; then
		warn "OPENFORTITRAY_RELEASE_URL is ignored on macOS; building the .app from the checkout (prebuilt bundles arrive with Fyne 5 packaging)."
	fi
	(cd "$REPO_DIR" && make app)
	[[ -d "$src" ]] || die "make app did not produce $src"
	sudo rm -rf "$APP_TARGET"
	sudo cp -R "$src" "$APP_TARGET"
	sudo chown -R root:wheel "$APP_TARGET"
	log "installed $APP_TARGET (menu-bar app, LSUIElement=1)"
}

install_binary() {
	if [[ "$OS" == Darwin ]]; then
		install_app_bundle
		return
	fi
	if [[ -n "$RELEASE_URL" ]]; then
		local tmp
		tmp="$(mktemp)"
		curl -fsSL "$RELEASE_URL" -o "$tmp"
		sudo install -o root -m 0755 "$tmp" "$BIN_TARGET"
		rm -f "$tmp"
		log "installed $BIN_TARGET from $RELEASE_URL"
	else
		(cd "$REPO_DIR" && make build)
		sudo install -o root -m 0755 "$REPO_DIR/openfortitray" "$BIN_TARGET"
		log "built from checkout and installed $BIN_TARGET"
	fi
}

# install_launcher installs the application-menu .desktop entry and its icon into
# the user's XDG data dirs so OpenFortiTray is searchable and launchable from the
# desktop's application menu. This is SEPARATE from the autostart .desktop that
# internal/autostart writes to ~/.config/autostart (that one only makes the app
# start at login). Linux only; on macOS the /Applications .app bundle already makes
# it Spotlight-searchable. Files land in the user's home, so writes go through
# as_principal to stay user-owned when the installer is run under sudo.
install_launcher() {
	local home data apps icons desktop_src icon_src desktop_target icon_target
	home="$(principal_home)"
	# XDG_DATA_HOME is honoured only when not root, for the same reason config_dir
	# honours XDG_CONFIG_HOME only then: under sudo it would be root's, not the user's.
	if [[ "$(id -u)" -ne 0 && -n "${XDG_DATA_HOME:-}" ]]; then
		data="$XDG_DATA_HOME"
	else
		data="$home/.local/share"
	fi
	apps="$data/applications"
	icons="$data/icons/hicolor/256x256/apps"
	desktop_src="$REPO_DIR/scripts/openfortitray.desktop"
	icon_src="$REPO_DIR/assets/icons/openfortitray-256.png"
	desktop_target="$apps/openfortitray.desktop"
	icon_target="$icons/openfortitray.png"

	[[ -f "$desktop_src" ]] || die "launcher template missing: $desktop_src"
	[[ -f "$icon_src" ]] || die "launcher icon missing: $icon_src"

	as_principal mkdir -p "$apps" "$icons"
	# The template's Exec is $BIN_TARGET (/usr/local/bin/openfortitray) — the same
	# path install_binary places the tray at on Linux; keep the two in step.
	as_principal cp "$desktop_src" "$desktop_target"
	as_principal chmod 0644 "$desktop_target"
	as_principal cp "$icon_src" "$icon_target"
	as_principal chmod 0644 "$icon_target"
	log "installed $desktop_target and $icon_target (application-menu entry)"

	# Best-effort cache refreshes so the entry and icon appear without a re-login.
	# Both tools are optional; a minimal or headless system may have neither.
	if command -v update-desktop-database >/dev/null 2>&1; then
		as_principal update-desktop-database "$apps" >/dev/null 2>&1 || true
	fi
	if command -v gtk-update-icon-cache >/dev/null 2>&1; then
		as_principal gtk-update-icon-cache -f -t "$data/icons/hicolor" >/dev/null 2>&1 || true
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

# verify checks the path the *app* will invoke, which is not necessarily the one
# this script just installed: the app reads "helper_path" from config.json and falls
# back to its own default. Checking HELPER_TARGET instead would report success for
# exactly the install that cannot work — helper and sudoers rule in one place, the
# app dialling another. install_config refuses that combination up front; this is the
# backstop for the case it cannot see, a config.json whose shape the sed in
# config_helper_path failed to read.
#
# The invocation goes through as_principal because the rule names $PRINCIPAL: when
# the whole installer was run under sudo we are already root, and a bare `sudo -n`
# would succeed for root no matter what /etc/sudoers.d/openfortitray says — a check that
# passes on a machine where the app is broken.
verify() {
	local app_helper
	app_helper="$(effective_helper_path)"
	if [[ "$app_helper" != "$HELPER_TARGET" ]]; then
		die "$(config_dir)/config.json points the app at $app_helper, but the helper and the sudoers rule were installed at $HELPER_TARGET. Set \"helper_path\": \"$HELPER_TARGET\" in that file, or re-run with OPENFORTITRAY_HELPER_DIR=$(dirname "$app_helper")"
	fi
	# "stop" with no tunnel running is a successful no-op, which makes it the
	# cheapest end-to-end check that the rule, the path and the mode all line up.
	as_principal sudo -n "$app_helper" stop >/dev/null 2>&1 ||
		die "'sudo -n $app_helper stop' still prompts or fails; the sudoers rule is not effective for $PRINCIPAL"
	log "verified passwordless invocation of $app_helper (the path the app dials) as $PRINCIPAL"
}

resolve_principal
validate_helper_dir
# Before anything privileged: a missing or malformed OPENFORTITRAY_GATEWAY, or a config
# that names a helper somewhere else, should cost a re-run rather than a
# half-finished install.
install_config
preflight_paths
install_openconnect
resolve_openconnect
install_binary
install_helper
install_sudoers
verify
if [[ "$OS" == Linux ]]; then
	install_launcher
fi

if [[ "$OS" == Darwin ]]; then
	log "done. Launch the app with: open '$APP_TARGET'  (or from Launchpad)"
else
	log "done. Launch the app with: $TRAY_TARGET &"
fi
log "Gateway lives in $(config_dir)/config.json; edit it there to point elsewhere."
log "First connect opens a browser window for the SAML login."
log "Quit FortiClient before connecting — two clients must not share the tunnel."
