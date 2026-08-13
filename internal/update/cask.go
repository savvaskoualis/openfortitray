package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// defaultCaskURL is the raw cask file in the Homebrew tap the macOS build is
// installed from. It is the tap's published state — the same content `brew
// update` will fetch — so reading it answers "can brew actually install this
// version yet?" without running brew or touching its local clone.
const defaultCaskURL = "https://raw.githubusercontent.com/savvaskoualis/homebrew-tap/main/Casks/openfortitray.rb"

// maxCaskBytes bounds the read: the cask is ~2 KB, so anything far larger is not
// the file we asked for and must not be slurped into memory.
const maxCaskBytes = 64 << 10

// caskVersionRE matches the cask's pinned version line, e.g. `  version "0.1.20"`.
var caskVersionRE = regexp.MustCompile(`(?m)^\s*version\s+"([^"]+)"`)

// CaskChecker reads the version currently published in the Homebrew tap.
//
// It exists because the update CHECK and the update APPLY use different sources
// on macOS: the check reads GitHub releases, while the apply runs `brew upgrade
// --cask`. A release published before the tap's cask is bumped is real but not
// yet installable — brew answers "the latest version is already installed" and
// nothing happens, which reads to the user as a broken updater. Gating the offer
// on the cask means the app only ever promises an update it can deliver.
type CaskChecker struct {
	HTTPClient *http.Client // required; caller injects (tests use httptest)
	URL        string       // default defaultCaskURL when empty
	UserAgent  string       // default defaultUserAgent when empty
}

// Version returns the version string pinned in the tap's cask (no leading "v").
func (c CaskChecker) Version(ctx context.Context) (string, error) {
	url := c.URL
	if url == "" {
		url = defaultCaskURL
	}
	ua := c.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("update: build cask request: %w", err)
	}
	req.Header.Set("User-Agent", ua)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("update: fetch cask: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update: unexpected status %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCaskBytes))
	if err != nil {
		return "", fmt.Errorf("update: read cask: %w", err)
	}
	m := caskVersionRE.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("update: no version line in cask at %s", url)
	}
	return string(m[1]), nil
}

// CaskHasTag reports whether the tap's cask has caught up to tag — i.e. whether
// `brew upgrade --cask` would actually install it.
//
// It is deliberately lenient about the direction of the comparison: the cask is
// "caught up" when it is not OLDER than tag. A cask that has moved AHEAD of the
// release we are looking at (the tap bumped while this check was in flight) is
// not a reason to withhold an update.
//
// Errors fail OPEN, returning true: a network hiccup reading the tap must not
// silently suppress a real update. The worst case of failing open is the old
// behaviour — brew reports nothing to do — whereas failing closed would hide
// updates whenever raw.githubusercontent.com is unreachable.
func CaskHasTag(ctx context.Context, c CaskChecker, tag string) bool {
	got, err := c.Version(ctx)
	if err != nil {
		return true
	}
	return !Newer(strings.TrimPrefix(got, "v"), tag)
}
