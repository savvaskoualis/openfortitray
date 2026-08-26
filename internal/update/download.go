package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DownloadAndVerify fetches asset into a private 0700 temp dir, streaming to a
// 0600 file while computing its SHA-256, and checks that digest against the
// entry for asset.Name inside the release's SHA256SUMS file (downloaded from
// sums). On any mismatch, missing entry, or error it removes the partial file
// (and its temp dir) and returns an error. On success it returns the path to
// the verified file (caller owns cleanup).
//
// This is a security boundary: the returned file is intended to be executed by
// a later task, so it must never return a path to unverified or mismatched
// bytes. Transport is HTTPS-only with no downgrade: the initial URLs must be
// https, and any redirect to a non-https scheme is rejected.
func (c Checker) DownloadAndVerify(ctx context.Context, asset, sums Asset) (path string, err error) {
	if err := requireHTTPS(sums.URL); err != nil {
		return "", fmt.Errorf("update: sums url: %w", err)
	}
	if err := requireHTTPS(asset.URL); err != nil {
		return "", fmt.Errorf("update: asset url: %w", err)
	}

	client := c.downloadClient()

	// 1. Fetch and parse SHA256SUMS first, then fail closed if our asset is
	// not listed. We do this before touching the (large) payload.
	sumsData, err := c.fetchAll(ctx, client, sums.URL)
	if err != nil {
		return "", fmt.Errorf("update: fetch SHA256SUMS: %w", err)
	}
	digests := parseSHA256SUMS(sumsData)
	expected, ok := digests[asset.Name]
	if !ok {
		return "", fmt.Errorf("update: %q is not listed in SHA256SUMS (fail closed)", asset.Name)
	}
	expected = strings.ToLower(expected)

	// The on-disk filename is derived from the asset name (release-controlled
	// text), and the applier interpolates the resulting path into a command line.
	// Constrain it to a plain, shell/PowerShell-safe filename BEFORE it can reach
	// any of that — fail closed on anything exotic. This is what makes the later
	// single-quoting sufficient rather than load-bearing on its own.
	name, err := safeAssetFilename(asset.Name)
	if err != nil {
		return "", err
	}

	// 2. Create a private temp dir (0700) and an exclusive 0600 file within it.
	dir, err := os.MkdirTemp("", "openfortitray-dl-")
	if err != nil {
		return "", fmt.Errorf("update: create temp dir: %w", err)
	}
	// From here on, any failure must scrub the whole directory so we never
	// leave unverified bytes on disk.
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	if err = os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("update: chmod temp dir: %w", err)
	}

	filePath := filepath.Join(dir, name)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("update: create temp file: %w", err)
	}

	// 3. Stream the payload to disk while hashing, without buffering it all in
	// memory.
	req, err := c.newGetRequest(ctx, asset.URL)
	if err != nil {
		_ = f.Close()
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		_ = f.Close()
		return "", fmt.Errorf("update: fetch asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = f.Close()
		return "", fmt.Errorf("update: unexpected status %d fetching %s", resp.StatusCode, asset.URL)
	}

	h := sha256.New()
	// Cap the stream well above any real installer (~10-25 MB). A compromised but
	// TLS-valid origin cannot exhaust the disk; a stream truncated at the cap
	// simply fails the sha gate below, so this never weakens verification.
	written, copyErr := io.Copy(f, io.TeeReader(io.LimitReader(resp.Body, maxAssetBytes), h))
	closeErr := f.Close()
	if copyErr != nil {
		err = fmt.Errorf("update: stream asset %q: %w", asset.Name, copyErr)
		return "", err
	}
	if closeErr != nil {
		err = fmt.Errorf("update: close temp file: %w", closeErr)
		return "", err
	}

	// 4. Optional defense in depth: byte count must match advertised size.
	if asset.Size > 0 && written != asset.Size {
		err = fmt.Errorf("update: size mismatch for %q: got %d bytes, expected %d", asset.Name, written, asset.Size)
		return "", err
	}

	// 5. The real gate: verify the digest. Never return a mismatched file.
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		err = fmt.Errorf("update: sha256 mismatch for %q: got %s, expected %s", asset.Name, got, expected)
		return "", err
	}

	return filePath, nil
}

// maxAssetBytes caps the downloaded payload stream. Real installers are
// ~10-25 MB; 512 MiB is comfortably above that and far below anything that
// could exhaust a disk.
const maxAssetBytes = 512 << 20

// safeAssetFilename returns the base filename of a release asset, but only if it
// is a plain, shell/PowerShell-safe name: [A-Za-z0-9._-], non-empty, not "." or
// ".." and not starting with a dot. Because this name becomes the on-disk file
// the applier later runs (its path is interpolated into a command), constraining
// it here — fail closed — is what keeps the applier's quoting from being the sole
// defense against release-controlled text.
func safeAssetFilename(assetName string) (string, error) {
	base := filepath.Base(assetName)
	if base == "" || base == "." || base == ".." || strings.HasPrefix(base, ".") {
		return "", fmt.Errorf("update: unsafe asset filename %q", assetName)
	}
	for _, r := range base {
		switch {
		case r >= '0' && r <= '9',
			r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r == '.', r == '_', r == '-':
		default:
			return "", fmt.Errorf("update: asset filename %q has an unsafe character %q", assetName, r)
		}
	}
	return base, nil
}

// downloadClient returns a shallow copy of the injected HTTP client with a
// CheckRedirect that refuses any redirect whose target scheme is not https.
// Copying avoids mutating the caller's client (and any client shared with the
// checker's API calls).
func (c Checker) downloadClient() *http.Client {
	base := c.HTTPClient
	if base == nil {
		base = http.DefaultClient
	}
	cp := *base
	// The download is bounded by the caller's context, not a whole-request
	// Timeout: http.Client.Timeout covers the entire body read, so a short
	// timeout inherited from the API client would abort a large installer
	// mid-stream. Clear it and let ctx govern.
	cp.Timeout = 0
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("update: refusing non-https redirect to %s", req.URL.Redacted())
		}
		return nil
	}
	return &cp
}

// newGetRequest builds a plain GET with User-Agent hygiene matching the checker.
func (c Checker) newGetRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("update: build request: %w", err)
	}
	ua := c.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	return req, nil
}

// fetchAll GETs rawURL and returns the full body. Used for the small
// SHA256SUMS file only.
func (c Checker) fetchAll(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	req, err := c.newGetRequest(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, rawURL)
	}
	return io.ReadAll(resp.Body)
}

// requireHTTPS returns an error unless rawURL parses and uses the https scheme.
func requireHTTPS(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("refusing non-https url %q", rawURL)
	}
	return nil
}

// parseSHA256SUMS maps filename -> lowercase hex digest. Input is `shasum -a
// 256` style output: each line is "<64-hex><space><space><name>". It tolerates
// a leading "*" binary-mode marker on the name, CRLF line endings, a trailing
// newline, and blank lines; malformed lines (bad hex length, missing name) are
// skipped.
func parseSHA256SUMS(data []byte) map[string]string {
	out := make(map[string]string)
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Split off the digest (first whitespace-delimited field); the rest,
		// after trimming leading whitespace and an optional binary marker, is
		// the filename.
		fields := strings.SplitN(line, " ", 2)
		if len(fields) != 2 {
			continue
		}
		digest := strings.ToLower(strings.TrimSpace(fields[0]))
		if len(digest) != 64 || !isHex(digest) {
			continue
		}
		name := strings.TrimSpace(fields[1])
		name = strings.TrimPrefix(name, "*")
		if name == "" {
			continue
		}
		out[name] = digest
	}
	return out
}

// isHex reports whether s consists solely of lowercase hex digits.
func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
