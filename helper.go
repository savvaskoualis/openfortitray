// Package openfortitray carries the privileged tunnel helper embedded in the
// signed binary and the first-run bootstrap that installs it via a native macOS
// admin-password prompt.
//
// A user who `brew install --cask`s the app gets OpenFortiTray.app and nothing
// else: the root-owned helper (/usr/local/libexec/openfortitray-tunnel) and the
// scoped sudoers rule that make the tunnel reachable are laid down only by
// scripts/install.sh (checkout) or scripts/install-helper.sh (curl|sudo bash).
// Until they exist the first Connect fails with tunnel.ErrPermanent. This package
// lets the app offer to do that privileged setup itself with ONE osascript
// "with administrator privileges" prompt — no terminal.
//
// SECURITY. The privileged install runs a shell script AS ROOT via osascript. The
// script is assembled here (buildRootInstallScript) and escaped for AppleScript
// (escapeAppleScript). Read those two functions and the SECURITY notes on them
// before touching anything: the escaping and the set of values interpolated into
// the root shell are the whole attack surface.
package openfortitray

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// helperScript is the exact text of scripts/openfortitray-tunnel, embedded so the
// signed binary IS the trusted source of the helper — no download, no checkout.
// It still carries the OPENCONNECT='@OPENCONNECT@' placeholder; the privileged
// install substitutes it with the resolved openconnect path exactly as
// scripts/install-helper.sh does.
//
//go:embed scripts/openfortitray-tunnel
var helperScript string

// HelperPath is where the helper is installed and what the sudoers rule is scoped
// to. It mirrors tunnel.DefaultHelperPath and config's default helper_path; the
// three must agree or sudo asks for a password and the tunnel fails.
const HelperPath = "/usr/local/libexec/openfortitray-tunnel"

// sudoersPath is the scoped NOPASSWD rule the install writes.
const sudoersPath = "/etc/sudoers.d/openfortitray"

// placeholderLine is the exact line in the embedded helper that the install
// rewrites with the resolved openconnect path. Must match scripts/openfortitray-tunnel.
const placeholderLine = `OPENCONNECT='@OPENCONNECT@'`

// helperProbeTimeout bounds the passwordless `sudo -n <helper> stop` readiness
// probe. When no tunnel is running `stop` is a no-op and returns at once; the cap
// only guards against sudo itself wedging.
const helperProbeTimeout = 12 * time.Second

// ErrUserCancelled reports that the user dismissed the macOS admin-password
// prompt. It is not a failure to surface as an error dialog — the user chose not
// to install — so the UI treats it quietly.
var ErrUserCancelled = errors.New("openfortitray: user cancelled the admin-password prompt")

// Charset gates for the only two dynamic values baked into the privileged shell
// script (plus the workdir path we ourselves create). Each excludes the single
// quote, the double quote, the backslash, whitespace and every shell
// metacharacter, so a validated value cannot break out of the single-quoted shell
// assignment it lands in, nor out of the AppleScript double-quoted string the
// whole script is escaped into.
var (
	principalRe   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	openconnectRe = regexp.MustCompile(`^[A-Za-z0-9._/+-]+$`)
	workdirRe     = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// validatePrincipal accepts only a plain local user name and never root: a
// sudoers rule naming root would grant the human nothing, and any character
// outside the class could alter the rule.
func validatePrincipal(p string) error {
	switch {
	case p == "":
		return errors.New("openfortitray: empty user name; cannot scope a sudoers rule")
	case p == "root":
		return errors.New("openfortitray: refusing to write a sudoers rule for root")
	case !principalRe.MatchString(p):
		return fmt.Errorf("openfortitray: user name %q has characters unsafe for a sudoers rule", p)
	}
	return nil
}

// validateOpenconnectPath accepts only an absolute path in a strict charset, so
// it is safe to bake into the root helper (mirrors resolve_openconnect in
// scripts/install-helper.sh).
func validateOpenconnectPath(p string) error {
	switch {
	case p == "":
		return errors.New("openfortitray: empty openconnect path")
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("openfortitray: openconnect path %q is not absolute", p)
	case !openconnectRe.MatchString(p):
		return fmt.Errorf("openfortitray: openconnect path %q has characters unsafe to embed in the helper", p)
	}
	return nil
}

// validateWorkdir gates the temp directory path we create and interpolate. We
// build it ourselves via os.MkdirTemp, so this only ever rejects a pathologically
// named TMPDIR — but it keeps the guarantee that nothing with a quote or
// metacharacter reaches the privileged string.
func validateWorkdir(p string) error {
	switch {
	case p == "":
		return errors.New("openfortitray: empty workdir")
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("openfortitray: workdir %q is not absolute", p)
	case !workdirRe.MatchString(p):
		return fmt.Errorf("openfortitray: workdir %q has unsafe characters", p)
	}
	return nil
}

// helperSHA256 is the lowercase hex sha256 of the embedded helper. The privileged
// install bakes it in and re-verifies the temp copy against it as root before
// trusting it — this is what keeps the embedded (signed) bytes, not the temp
// file, the source of truth, and closes any swap of the temp file between the
// unprivileged write and the root read.
func helperSHA256() string {
	sum := sha256.Sum256([]byte(helperScript))
	return hex.EncodeToString(sum[:])
}

// escapeAppleScript escapes an arbitrary string so it can be embedded verbatim
// inside an AppleScript double-quoted literal, i.e. between the quotes of
//
//	do shell script "<here>" with administrator privileges
//
// AppleScript string literals interpret a fixed set of backslash escapes, so the
// reverse mapping reconstructs the ORIGINAL bytes when AppleScript hands the
// string to /bin/sh. The order matters: backslash MUST be doubled first, or the
// backslashes introduced for the other escapes would themselves be re-escaped.
// After that, quotes and the whitespace controls are independent single-rune
// substitutions.
//
// This is applied to the WHOLE shell script — the fixed template plus the
// single-quoted safe tokens — so the shell script's own quotes, backslashes and
// newlines survive the trip through AppleScript unchanged.
func escapeAppleScript(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/8 + 1)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// buildRootInstallScript assembles the self-contained POSIX-sh script that runs
// AS ROOT to install the helper and the sudoers rule.
//
// SECURITY — what is interpolated into the privileged shell, and why it is safe:
//
//   - principal    the invoking GUI user (os/user.Current), validated ^[A-Za-z0-9._-]+$
//   - openconnect  the resolved absolute openconnect path, validated ^[A-Za-z0-9._/+-]+$
//   - workdir      a temp dir WE create (os.MkdirTemp), validated ^[A-Za-z0-9._/-]+$
//   - the embedded helper's sha256 (64 hex chars, computed here)
//
// Each of the first three is emitted as a SINGLE-QUOTED shell assignment at the
// top of the script. None can contain a single quote, so none can close its
// assignment; none can contain a double quote, a backslash or a newline, so none
// perturbs the AppleScript escaping applied to the whole script afterwards. NO
// gateway, config, cookie or any other user-typed value is ever interpolated: the
// helper's own text (which does carry quotes/backslashes) travels in a temp file
// written by the unprivileged app and is verified as root against the baked-in
// sha256 before use — it is never spliced into this string. The body below is a
// fixed constant.
func buildRootInstallScript(principal, openconnect, workdir string) (string, error) {
	if err := validatePrincipal(principal); err != nil {
		return "", err
	}
	if err := validateOpenconnectPath(openconnect); err != nil {
		return "", err
	}
	if err := validateWorkdir(workdir); err != nil {
		return "", err
	}
	if n := strings.Count(helperScript, placeholderLine); n != 1 {
		return "", fmt.Errorf("openfortitray: embedded helper must contain exactly one %q line, found %d", placeholderLine, n)
	}
	header := "PRINCIPAL='" + principal + "'\n" +
		"OC='" + openconnect + "'\n" +
		"EXPECTED_SHA='" + helperSHA256() + "'\n" +
		"WORKDIR='" + workdir + "'\n"
	return header + rootInstallerBody, nil
}

// rootInstallerBody is the fixed, value-free tail of the privileged script. It
// mirrors scripts/install-helper.sh's install_helper / install_sudoers / verify,
// rewritten in POSIX sh because `do shell script` runs the string under /bin/sh.
// It reads $PRINCIPAL/$OC/$EXPECTED_SHA/$WORKDIR set by the header above; it
// interpolates nothing itself.
const rootInstallerBody = `set -eu
PATH=/usr/bin:/bin:/usr/sbin:/sbin
export PATH

HELPER_DIR=/usr/local/libexec
HELPER_TARGET="$HELPER_DIR/openfortitray-tunnel"
SUDOERS_TARGET=/etc/sudoers.d/openfortitray
SRC="$WORKDIR/openfortitray-tunnel"

die() { echo "openfortitray-install: $1" >&2; exit 1; }

# Defence in depth: re-validate the interpolated tokens inside the root shell too.
# They were charset-checked in Go before being single-quoted into this script; a
# mistake there still cannot smuggle a metacharacter into a root context here.
case "$PRINCIPAL" in
  '' | root) die "refusing to write a sudoers rule for '$PRINCIPAL'" ;;
  *[!A-Za-z0-9._-]*) die "user name has invalid characters" ;;
esac
case "$OC" in
  /*) ;;
  *) die "openconnect path must be absolute" ;;
esac
case "$OC" in
  *[!A-Za-z0-9._/+-]*) die "openconnect path has invalid characters" ;;
esac
case "$EXPECTED_SHA" in
  '' | *[!0-9a-f]*) die "bad expected sha" ;;
esac
[ -x "$OC" ] || die "$OC is not executable"

# The helper text was written by the unprivileged app to $SRC.
[ -f "$SRC" ] || die "helper source $SRC is missing"

# Root-owned scratch dir so nothing we stage can be swapped between validation and
# install. Clean up this run's staging file too, in case we die after staging.
RTMP=$(mktemp -d) || die "cannot create a temp dir"
trap 'rm -rf "$RTMP"; rm -f "$HELPER_DIR/.openfortitray-tunnel.tmp.$$"' EXIT

# Copy the user-written helper into the root-owned scratch dir ONCE, then both
# verify and build from THAT copy. Re-opening the user-writable $SRC after hashing
# would let the bytes we hashed differ from the bytes we install; hashing and
# installing must read the identical, root-owned copy. Verify it byte for byte
# against the hash the signed app computed from its embedded copy before trusting
# it as root: the embedded (signed) bytes, not the temp file, are the source of
# truth.
cp "$SRC" "$RTMP/src" || die "cannot copy helper source"
actual=$(shasum -a 256 "$RTMP/src" | awk '{print $1}')
[ "$actual" = "$EXPECTED_SHA" ] || die "helper source hash mismatch (expected $EXPECTED_SHA, got $actual)"

# Bake the resolved openconnect path into the (verified, root-owned) copy,
# replacing the single placeholder line. A line-by-line copy (never sed) keeps
# path characters from being reinterpreted; the count guard mirrors
# scripts/install-helper.sh.
subs=0
while IFS= read -r line; do
  if [ "$line" = "OPENCONNECT='@OPENCONNECT@'" ]; then
    printf "OPENCONNECT='%s'\n" "$OC"
    subs=$((subs + 1))
  else
    printf '%s\n' "$line"
  fi
done < "$RTMP/src" > "$RTMP/built"
[ "$subs" -eq 1 ] || die "expected exactly one @OPENCONNECT@ placeholder, substituted $subs"
sh -n "$RTMP/built" || die "generated helper is not valid shell"

# check_path prints why a path cannot be trusted on a passwordless-root chain, or
# nothing. find (not stat) so the -perm/-user predicates are BSD/GNU portable.
check_path() {
  if [ -n "$(find "$1" -maxdepth 0 ! -user root -print 2>/dev/null)" ]; then
    echo "$1 is not owned by root"
  elif [ -n "$(find "$1" -maxdepth 0 \( -perm -0020 -o -perm -0002 \) -print 2>/dev/null)" ]; then
    echo "$1 is group- or other-writable"
  fi
}

# walk_up dies unless $1 and every directory above it are root-owned and not
# writable by others: a helper reachable through a user-writable directory would
# be passwordless root for whoever can write there.
walk_up() {
  _p="$1"
  while :; do
    _problem=$(check_path "$_p")
    [ -z "$_problem" ] || die "$_problem; anything on a passwordless-root path must be root-owned and not writable by others (try: sudo chown root:wheel $HELPER_DIR && sudo chmod 755 $HELPER_DIR)"
    [ "$_p" = "/" ] && break
    _p=$(dirname "$_p")
  done
}

install -d -o root -g wheel -m 0755 "$HELPER_DIR" || die "cannot create $HELPER_DIR"

# Walk the LITERAL ancestry and the fully symlink-resolved ancestry (pwd -P): a
# root-owned symlink pointing into a user-writable directory would otherwise pass
# the literal walk while redirecting the real install target. This matches the
# double walk scripts/install-helper.sh does with readlink -f.
walk_up "$HELPER_DIR"
RESOLVED=$(cd "$HELPER_DIR" 2>/dev/null && pwd -P) || RESOLVED="$HELPER_DIR"
[ "$RESOLVED" = "$HELPER_DIR" ] || walk_up "$RESOLVED"

# Remove any stale staging file a previously crashed run left behind, then land
# the helper atomically: stage into the root-owned target dir, then rename.
rm -f "$HELPER_DIR"/.openfortitray-tunnel.tmp.* 2>/dev/null || true
STAGED="$HELPER_DIR/.openfortitray-tunnel.tmp.$$"
install -o root -g wheel -m 0755 "$RTMP/built" "$STAGED" || die "cannot stage the helper"
mv -f "$STAGED" "$HELPER_TARGET" || { rm -f "$STAGED"; die "cannot install $HELPER_TARGET"; }

# NOPASSWD rule scoped to the helper path ONLY (never openconnect, whose
# --script/--csd-wrapper would be passwordless root). Validate with visudo -c on a
# root-owned temp file BEFORE it goes live — a broken /etc/sudoers.d file can lock
# everyone out of sudo — then install 0440.
RULE="$PRINCIPAL ALL=(root) NOPASSWD: $HELPER_TARGET"
printf '%s\n' "$RULE" > "$RTMP/sudoers"
chmod 0440 "$RTMP/sudoers"
visudo -c -f "$RTMP/sudoers" >/dev/null || die "sudoers validation failed; nothing installed"
install -o root -g wheel -m 0440 "$RTMP/sudoers" "$SUDOERS_TARGET" || die "cannot install $SUDOERS_TARGET"

# Real end-to-end check: as the (non-root) principal, 'sudo -n <helper> stop' must
# succeed passwordlessly. Going through 'sudo -u' matters — a bare 'sudo -n' as
# root would pass regardless of the rule.
sudo -u "$PRINCIPAL" sudo -n "$HELPER_TARGET" stop >/dev/null 2>&1 || die "verification failed: 'sudo -n $HELPER_TARGET stop' still prompts for $PRINCIPAL"

echo "openfortitray-install: installed $HELPER_TARGET and $SUDOERS_TARGET for $PRINCIPAL"
`

// resolveOpenconnect finds the absolute openconnect path to bake into the helper.
// GUI apps launched from Finder often carry a minimal PATH that omits the
// Homebrew prefixes, so an OPENFORTITRAY_OPENCONNECT override and the usual
// absolute locations are probed in addition to a PATH lookup.
func resolveOpenconnect() (string, error) {
	if env := os.Getenv("OPENFORTITRAY_OPENCONNECT"); env != "" {
		if err := validateOpenconnectPath(env); err != nil {
			return "", err
		}
		if fi, err := os.Stat(env); err != nil || fi.IsDir() {
			return "", fmt.Errorf("openfortitray: OPENFORTITRAY_OPENCONNECT %q is not a usable file", env)
		}
		return env, nil
	}
	var candidates []string
	if p, err := exec.LookPath("openconnect"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates,
		"/opt/homebrew/bin/openconnect",
		"/usr/local/bin/openconnect",
		"/usr/bin/openconnect",
		"/usr/sbin/openconnect",
	)
	for _, c := range candidates {
		if !strings.HasPrefix(c, "/") {
			continue // a bare PATH hit tells us nothing to bake in
		}
		fi, err := os.Stat(c)
		if err != nil || fi.IsDir() {
			continue
		}
		if validateOpenconnectPath(c) != nil {
			continue
		}
		return c, nil
	}
	return "", errors.New("openfortitray: openconnect not found; install it (brew install openconnect) and try again")
}

// currentPrincipal is the GUI user the sudoers rule is written for. The app runs
// as that user, so os/user.Current is exactly the invoking human.
func currentPrincipal() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("openfortitray: cannot determine the current user: %w", err)
	}
	if err := validatePrincipal(u.Username); err != nil {
		return "", err
	}
	return u.Username, nil
}

// RequiredHelperABI is the app<->helper contract this build needs. The installed
// helper prints its own with `openfortitray-tunnel abi`, and a mismatch means the
// helper predates something the app depends on, so the bootstrap must reinstall it.
//
// ABI 2: the helper stopped handing the cookie to openconnect via
// --cookie-on-stdin, whose 1024-byte read buffer silently truncated the longer
// cookies this gateway issues (1288 bytes observed) — producing an opaque "Cookie
// was rejected by server" that looked for a long time like a gateway fault. An
// ABI-1 helper cannot connect with such a cookie at all, so shipping the app
// without upgrading the helper would fix nothing.
const RequiredHelperABI = 2

// HelperReady reports whether the privileged helper is installed, callable
// passwordlessly by this user, AND current enough for this build — the real
// end-to-end check, not just a file stat. It is what decides whether the
// first-run bootstrap is needed. `stop` on no running tunnel is a no-op, so the
// probe is cheap and side-effect-free.
func HelperReady() bool {
	return helperReadyAt(HelperPath, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), helperProbeTimeout)
		defer cancel()
		return exec.CommandContext(ctx, "sudo", "-n", HelperPath, "stop").Run()
	}) && helperABIAt(HelperPath) >= RequiredHelperABI
}

// helperABIAt returns the ABI the installed helper reports, or 0 if it cannot be
// determined — which is the case for every helper predating the `abi` subcommand
// (they exit non-zero on an unknown one). 0 is below any requirement, so an
// unknown helper is treated as outdated and gets reinstalled.
func helperABIAt(path string) int {
	ctx, cancel := context.WithTimeout(context.Background(), helperProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sudo", "-n", path, "abi").Output()
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// helperReadyAt is the testable core of HelperReady: the helper file must exist
// and the injected passwordless-stop probe must succeed.
func helperReadyAt(path string, stop func() error) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return stop() == nil
}

// installWith is the idempotency gate shared by every platform's Install: if the
// helper is already ready, do nothing; otherwise run the privileged install.
func installWith(ready func() bool, run func() error) error {
	if ready() {
		return nil
	}
	return run()
}

// isUserCancel recognises osascript's user-cancel reply so a dismissed password
// prompt is reported as ErrUserCancelled rather than a scary failure. osascript
// emits, on stderr, e.g.  "… execution error: User canceled. (-128)". Match that
// specific signal — the parenthesised "(-128)" error form, or the literal
// "User cancel(l)ed" text — NOT a bare "-128" substring, which could appear in an
// unrelated failure's output.
func isUserCancel(out string) bool {
	if strings.Contains(out, "(-128)") {
		return true
	}
	lower := strings.ToLower(out)
	return strings.Contains(lower, "user canceled") || strings.Contains(lower, "user cancelled")
}
