#!/usr/bin/env bash
# install-helper.sh — set up ONLY the privileged helper + sudoers rule that the
# OpenFortiTray tunnel needs, WITHOUT a git checkout.
#
# The .dmg and `brew install --cask openfortitray` lay down OpenFortiTray.app and
# nothing else. The tunnel is brought up by a small root-owned helper reached
# through a scoped sudoers rule (see scripts/openfortitray-tunnel and the Privilege
# section of README.md); the app install ships neither, so the first Connect fails
# until they exist. scripts/install.sh does this too, but sources the helper from a
# checkout ($REPO_DIR/scripts/openfortitray-tunnel) — a .dmg/cask user has none.
# This script downloads that one file over HTTPS, pinned to a release tag and
# checksum-verified, and installs the helper + rule. It is the checkout-free half of
# install.sh, not a replacement: it does not touch the app, config.json or the
# gateway.
#
# RUN IT AS ROOT — it IS the privileged step:
#
#   curl -fsSL https://raw.githubusercontent.com/savvaskoualis/openfortitray/v0.1.16/scripts/install-helper.sh | sudo bash
#
# `sudo` sets SUDO_USER to you; the sudoers rule is written for that user, never for
# root (a rule naming root would grant the human nothing).
#
# INTEGRITY — what is verified vs what is trusted (read before widening):
#
#   Verified: the downloaded helper's sha256 is compared against EXPECTED_SHA256, a
#   value pinned in this file to the bytes of scripts/openfortitray-tunnel at tag
#   $VERSION. A mismatch aborts before anything is installed. This is meaningful
#   because the pin travels with the tag: the same immutable tag serves the same
#   bytes, and raw.githubusercontent.com serves a tag's blob content verbatim.
#
#   Trusted: TLS (that GitHub is who it says and the bytes arrived intact), and that
#   the release process bumped EXPECTED_SHA256 in lockstep when it moved $VERSION.
#   Overriding OPENFORTITRAY_REF to a *different* ref invalidates the built-in pin
#   (the hash is only correct for $VERSION); in that case you MUST supply the matching
#   hash via OPENFORTITRAY_HELPER_SHA256 or the download runs checksum-UNVERIFIED
#   (TLS + tag only) with a loud warning. Pointing OPENFORTITRAY_REF at a branch
#   rather than a tag means the bytes can change under you — don't, for anything but
#   local testing.
#
# THREAT MODEL is identical to scripts/install.sh and scripts/openfortitray-tunnel:
# the sudoers rule grants the invoking user passwordless root for ONE script, which
# validates its own arguments (argument injection into openconnect is blocked). What
# is not uniformly enforceable is binary tampering: the helper runs an absolute
# openconnect path baked in below, and this script checks that binary and every
# directory above it are root-owned and not group/other-writable. On Linux a failure
# aborts; on macOS it warns and continues, because Homebrew owns its prefix as the
# installing user by design (on a single-user mac that user is already an admin who
# can run sudo directly; on a shared mac, install openconnect somewhere root-owned
# and pass OPENFORTITRAY_OPENCONNECT). A documented boundary, not a claim of safety.
#
# Knobs:
#   OPENFORTITRAY_REF=<tag>              # ref to download from (default $VERSION); use a TAG, not a branch
#   OPENFORTITRAY_HELPER_SHA256=<hex>    # expected sha256 of the helper (required when REF != default)
#   OPENFORTITRAY_OPENCONNECT=/path      # use this openconnect, not the one found on PATH
set -euo pipefail

# Fixed PATH: the caller's environment must never influence what this privileged
# script runs. System dirs plus the two Homebrew prefixes, because when invoked
# under `sudo` root's PATH usually omits them and that is where a Mac's openconnect
# and package tools live.
PATH=/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin:/opt/homebrew/sbin
export PATH

# VERSION is the release this installer belongs to and the tag it downloads from by
# default. EXPECTED_SHA256 is the sha256 of scripts/openfortitray-tunnel AT THAT TAG.
# THE RELEASE PROCESS MUST BUMP BOTH TOGETHER: cutting a new tag that changes the
# helper without updating this hash makes every checkout-free install abort (which is
# the safe failure), and updating the hash without the tag serves stale bytes.
VERSION=v0.1.16
EXPECTED_SHA256=a08c5e65a3627319182f67d22cb34aa53618fd77141d33400b2cce3f34e6b76c

REF="${OPENFORTITRAY_REF:-$VERSION}"
RAW_BASE=https://raw.githubusercontent.com/savvaskoualis/openfortitray
HELPER_URL="$RAW_BASE/$REF/scripts/openfortitray-tunnel"

# Fixed install locations. Unlike install.sh these are NOT overridable: relocating the
# helper only matters when config.json names a non-default helper_path, and this
# checkout-free installer deliberately never reads or writes the app's config.
HELPER_DIR=/usr/local/libexec
HELPER_TARGET="$HELPER_DIR/openfortitray-tunnel"
SUDOERS_TARGET=/etc/sudoers.d/openfortitray
OS="$(uname -s)"

# Temp files live in a single dir we own and wipe on exit, so a failed download or a
# rejected checksum never leaves a partial helper behind.
WORKDIR=""
cleanup() { [[ -n "$WORKDIR" && -d "$WORKDIR" ]] && rm -rf "$WORKDIR"; }
trap cleanup EXIT

log() { printf 'install-helper: %s\n' "$1"; }
warn() { printf 'install-helper: WARNING: %s\n' "$1" >&2; }
die() {
	printf 'install-helper: error: %s\n' "$1" >&2
	exit 1
}

case "$OS" in
Darwin) ROOT_GROUP=wheel ;;
Linux) ROOT_GROUP=root ;;
*) die "unsupported OS: $OS" ;;
esac

# require_root: this script IS the privileged step, so unlike install.sh it must be
# run as root. Piping into `sudo bash` is the intended path and sets SUDO_USER, which
# resolve_principal needs to name the rule.
require_root() {
	[[ "$(id -u)" -eq 0 ]] && return 0
	printf 'install-helper: this installs a root-owned helper and a sudoers rule; run it as root:\n' >&2
	printf '  curl -fsSL %s/%s/scripts/install-helper.sh | sudo bash\n' "$RAW_BASE" "$VERSION" >&2
	exit 1
}

# resolve_principal decides which user the sudoers rule names. We are always root
# here, so it must come from SUDO_USER: a rule for root grants the human nothing.
resolve_principal() {
	local user="${SUDO_USER:-}"
	[[ -n "$user" ]] ||
		die "SUDO_USER is empty — run via 'sudo' (e.g. 'curl ... | sudo bash'), not as a raw root shell"
	[[ "$user" =~ ^[A-Za-z0-9._-]+$ ]] ||
		die "refusing to write a sudoers rule for user name '$user': unexpected characters"
	[[ "$user" != root ]] || die "refusing to write a sudoers rule for root"
	PRINCIPAL="$user"
}

# stat_field prints one attribute of a path. BSD and GNU stat disagree on flags, so
# the difference is confined here. Both default to lstat (no symlink follow), which is
# what the walk below wants. (Copied from scripts/install.sh — keep in step.)
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
# root, or nothing if it is fine. (Copied from scripts/install.sh.)
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

# walk_chain prints a problem line for $1 and every directory above it. Non-existent
# leaves are skipped: this installer creates them root-owned. (Copied from install.sh.)
walk_chain() {
	local p="$1"
	while [[ ! -e "$p" && ! -L "$p" && "$p" != / ]]; do p="$(dirname "$p")"; done
	while :; do
		path_unsafe "$p"
		[[ "$p" == / ]] && break
		p="$(dirname "$p")"
	done
}

# check_chain reports whether $1 is safe to reach from a passwordless-root context.
# $2 is "abort" or "warn"; returns non-zero when anything was flagged. It walks twice
# — literally (to catch a group-writable symlink) and fully resolved (to catch a
# root-owned symlink into a user-writable dir). (Copied from scripts/install.sh, with
# a remedy message adapted to this installer's fixed HELPER_DIR.)
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
		printf 'install-helper: error: %s\n' "$problems" >&2
		printf 'install-helper: error: %s\n' \
			"anything reachable from a passwordless-root path must be root-owned and not writable by others." \
			"Remedy: sudo chown root:$ROOT_GROUP $HELPER_DIR && sudo chmod 755 $HELPER_DIR" \
			"(an Intel-mac Homebrew prefix leaves /usr/local user-owned)." >&2
		exit 1
	fi
	while IFS= read -r problem; do warn "$problem"; done <<<"$problems"
	return 1
}

# ensure_openconnect installs openconnect when it is missing. On Linux we are root, so
# the package manager runs directly. On macOS Homebrew refuses to run as root, and the
# brew cask already `depends_on openconnect`, so we only check and warn — never
# hard-fail — telling the user how to add it themselves.
ensure_openconnect() {
	if command -v openconnect >/dev/null 2>&1 || [[ -n "${OPENFORTITRAY_OPENCONNECT:-}" ]]; then
		return 0
	fi
	case "$OS" in
	Linux)
		if command -v apt-get >/dev/null 2>&1; then apt-get install -y openconnect || warn "apt-get failed to install openconnect; install it manually"
		elif command -v dnf >/dev/null 2>&1; then dnf install -y openconnect || warn "dnf failed to install openconnect; install it manually"
		elif command -v pacman >/dev/null 2>&1; then pacman -S --noconfirm openconnect || warn "pacman failed to install openconnect; install it manually"
		else warn "no supported package manager found; install openconnect manually"
		fi
		;;
	Darwin)
		warn "openconnect not found. The brew cask depends on it, but the .dmg does not bundle it."
		warn "Install it as your normal user (Homebrew will not run as root): brew install openconnect"
		warn "Then re-run this installer."
		;;
	esac
}

# resolve_openconnect picks the absolute openconnect path to bake into the helper and
# verifies nothing writable sits on the way to it. NOT resolved through symlinks: the
# stable /opt/homebrew/bin path survives `brew upgrade` where the Cellar target does
# not; check_chain examines both anyway. (Logic copied from scripts/install.sh.)
resolve_openconnect() {
	local p
	p="${OPENFORTITRAY_OPENCONNECT:-$(command -v openconnect || true)}"
	# When invoked under sudo, PATH lookup can still miss a Homebrew install; probe the
	# usual absolute locations before giving up.
	if [[ -z "$p" ]]; then
		local cand
		for cand in /opt/homebrew/bin/openconnect /usr/local/bin/openconnect /usr/bin/openconnect /usr/sbin/openconnect; do
			[[ -x "$cand" ]] && {
				p="$cand"
				break
			}
		done
	fi
	[[ -n "$p" ]] || die "openconnect not found; install it and re-run (see the warnings above)"
	[[ "$p" == /* ]] || die "openconnect path must be absolute, got '$p'"
	[[ -x "$p" ]] || die "$p is not executable"
	# Baked into a single-quoted assignment in a script that runs as root: refuse
	# anything that could break out of it.
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

# sha256_hex prints the lowercase sha256 of $1, using whichever tool exists.
sha256_hex() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		die "no sha256 tool (shasum or sha256sum) found; cannot verify the download"
	fi
}

# download_helper fetches scripts/openfortitray-tunnel over HTTPS at $REF and verifies
# its integrity BEFORE it is used. The checksum is the security boundary — see the
# INTEGRITY note at the top for what is verified vs trusted. Leaves the pristine
# (placeholder-bearing) download at $DOWNLOADED.
download_helper() {
	command -v curl >/dev/null 2>&1 || die "curl is required to download the helper"
	# REF is embedded in a URL; refuse anything with characters that could alter it,
	# and refuse a leading '/' or any '..' so a non-default ref can't redirect the raw
	# URL outside the repo (e.g. '../other/repo/ref').
	[[ "$REF" =~ ^[A-Za-z0-9._/-]+$ ]] || die "OPENFORTITRAY_REF contains unsafe characters: '$REF'"
	[[ "$REF" != /* ]] || die "OPENFORTITRAY_REF must not start with '/': '$REF'"
	[[ "$REF" != *..* ]] || die "OPENFORTITRAY_REF must not contain '..': '$REF'"

	WORKDIR="$(mktemp -d)"
	DOWNLOADED="$WORKDIR/openfortitray-tunnel"
	log "downloading helper from $HELPER_URL"
	curl -fsSL --proto '=https' --tlsv1.2 "$HELPER_URL" -o "$DOWNLOADED" ||
		die "download failed: $HELPER_URL (is OPENFORTITRAY_REF=$REF a real tag?)"
	[[ -s "$DOWNLOADED" ]] || die "downloaded helper is empty"

	# Decide which expected hash applies. The built-in pin is only correct for the
	# default $VERSION; an overridden ref needs its own hash via the env var.
	local expected=""
	if [[ -n "${OPENFORTITRAY_HELPER_SHA256:-}" ]]; then
		expected="${OPENFORTITRAY_HELPER_SHA256,,}"
		[[ "$expected" =~ ^[0-9a-f]{64}$ ]] ||
			die "OPENFORTITRAY_HELPER_SHA256 must be 64 hex characters"
	elif [[ "$REF" == "$VERSION" ]]; then
		expected="$EXPECTED_SHA256"
	fi

	local actual
	actual="$(sha256_hex "$DOWNLOADED")"
	if [[ -n "$expected" ]]; then
		[[ "$actual" == "$expected" ]] ||
			die "helper checksum mismatch: expected $expected, got $actual — refusing to install a helper whose bytes changed under the pin"
		log "verified helper sha256 $actual"
	else
		warn "no pinned checksum for ref '$REF' (the built-in pin is only valid for $VERSION)."
		warn "Installing with TLS + tag trust only; sha256 was $actual."
		warn "For a verified install, pass OPENFORTITRAY_HELPER_SHA256=<hex> matching that ref."
	fi

	# Same guarantee install.sh relies on: exactly one @OPENCONNECT@ line to substitute.
	local n
	n="$(grep -c "^OPENCONNECT='@OPENCONNECT@'$" "$DOWNLOADED" || true)"
	[[ "$n" == 1 ]] ||
		die "expected exactly one @OPENCONNECT@ placeholder in the downloaded helper, found $n"
}

# install_helper bakes the verified openconnect path into the download and installs it
# root-owned 0755 into a root-owned dir, atomically (write beside the target, then
# rename). Mirrors scripts/install.sh's install_helper, minus the checkout source.
install_helper() {
	local built subs line
	built="$WORKDIR/openfortitray-tunnel.built"
	subs=0
	while IFS= read -r line; do
		if [[ "$line" == "OPENCONNECT='@OPENCONNECT@'" ]]; then
			printf "OPENCONNECT='%s'\n" "$OPENCONNECT_PATH"
			subs=$((subs + 1))
		else
			printf '%s\n' "$line"
		fi
	done <"$DOWNLOADED" >"$built"
	[[ "$subs" -eq 1 ]] || die "expected exactly one @OPENCONNECT@ placeholder, substituted $subs"
	sh -n "$built" || die "generated helper is not valid shell"

	# Create the dir root-owned first, then re-check the whole chain now that it exists,
	# then land the file atomically via a same-dir rename.
	install -d -o root -g "$ROOT_GROUP" -m 0755 "$HELPER_DIR"
	check_chain "$HELPER_TARGET" abort || true
	local staged="$HELPER_DIR/.openfortitray-tunnel.$$.tmp"
	install -o root -g "$ROOT_GROUP" -m 0755 "$built" "$staged"
	mv -f "$staged" "$HELPER_TARGET"
	log "installed $HELPER_TARGET (root:$ROOT_GROUP, 0755, openconnect=$OPENCONNECT_PATH)"
}

# install_sudoers writes the NOPASSWD rule scoped to the helper path only (never
# openconnect: a NOPASSWD on openconnect is passwordless root via --script=). Validated
# with `visudo -c -f` on a temp file BEFORE it is activated — a broken file in
# /etc/sudoers.d can lock everyone out of sudo. Idempotent: re-running rewrites the
# identical rule. (Mirrors scripts/install.sh's install_sudoers; we are already root so
# no `sudo` prefix.)
install_sudoers() {
	local rule tmp
	rule="$PRINCIPAL ALL=(root) NOPASSWD: $HELPER_TARGET"
	tmp="$WORKDIR/sudoers"
	printf '%s\n' "$rule" >"$tmp"
	chmod 0440 "$tmp"
	visudo -c -f "$tmp" >/dev/null || die "sudoers validation failed; nothing installed"
	install -o root -g "$ROOT_GROUP" -m 0440 "$tmp" "$SUDOERS_TARGET"
	log "installed $SUDOERS_TARGET: $rule"
}

# verify is the real end-to-end check: as $PRINCIPAL (not root), `sudo -n <helper>
# stop` must succeed passwordlessly. `stop` with no tunnel running is a no-op, so it is
# the cheapest way to prove the rule, the path and the mode all line up. Going through
# `sudo -u $PRINCIPAL` matters: a bare `sudo -n` as root would pass regardless of what
# the rule says. (Mirrors scripts/install.sh's verify.)
verify() {
	sudo -u "$PRINCIPAL" sudo -n "$HELPER_TARGET" stop >/dev/null 2>&1 ||
		die "'sudo -n $HELPER_TARGET stop' still prompts or fails; the sudoers rule is not effective for $PRINCIPAL"
	log "verified passwordless invocation of $HELPER_TARGET as $PRINCIPAL"
}

# main wraps the whole invocation sequence in one function called on the LAST line, so
# a partially-transferred `curl | sudo bash` cannot execute a truncated prefix of these
# commands — bash does not start running until the closing brace and the final call
# have both arrived. Standard hygiene for a piped installer.
main() {
	require_root
	resolve_principal
	# Path safety of the (possibly not-yet-existing) helper location, before anything
	# is downloaded or installed.
	check_chain "$HELPER_TARGET" abort || true
	ensure_openconnect
	resolve_openconnect
	download_helper
	install_helper
	install_sudoers
	verify

	log "done — the privileged helper and sudoers rule for '$PRINCIPAL' are in place."
	log "Next steps:"
	log "  1. Quit FortiClient if it is running — two clients must not share the tunnel."
	log "  2. Open OpenFortiTray (from /Applications on macOS, or launch the binary on Linux)."
	log "  3. Open Settings…, set your gateway (host:port), and click Connect."
	log "This installer did not touch the app or config.json; the gateway is set in the app."
}

main "$@"
