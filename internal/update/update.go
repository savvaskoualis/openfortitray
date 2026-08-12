// Package update queries the GitHub releases API to discover whether a newer
// release of OpenFortiTray is available than the running build.
//
// This package is intentionally pure: it performs no downloads of binaries, no
// file writes, no process execution, and touches no UI. It only fetches the
// latest release metadata, parses it, and compares semantic versions. It is the
// first of three pieces of the one-click updater; a security-reviewed downloader
// consumes it next, so it stays stdlib-only with no side effects.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// defaultAPIBase is the GitHub REST API root used when Checker.APIBase is empty.
const defaultAPIBase = "https://api.github.com"

// defaultUserAgent is sent when Checker.UserAgent is empty. The GitHub API
// requires a User-Agent header on every request.
const defaultUserAgent = "openfortitray-updater"

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name string // e.g. "OpenFortiTray-v0.1.8.dmg", "OpenFortiTray-0.1.8-Setup.exe", "SHA256SUMS"
	URL  string // browser_download_url
	Size int64
}

// Release is the subset of the GitHub release we care about.
type Release struct {
	Tag    string // tag_name, e.g. "v0.1.8"
	Assets []Asset
}

// Checker fetches release information from the GitHub API.
type Checker struct {
	HTTPClient *http.Client // required; caller injects (tests use httptest)
	APIBase    string       // default "https://api.github.com" when empty
	Repo       string       // e.g. "savvaskoualis/openfortitray"
	UserAgent  string       // GitHub API requires a UA; default a sensible one when empty
}

// githubRelease mirrors only the fields we parse from the GitHub JSON response.
type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// Latest fetches the newest published release. It GETs
// {APIBase}/repos/{Repo}/releases/latest with Accept: application/vnd.github+json
// and the User-Agent, honouring ctx. Non-200 is an error including the status.
//
// The /releases/latest endpoint already excludes drafts and prereleases, so no
// extra filtering is needed here.
func (c Checker) Latest(ctx context.Context) (*Release, error) {
	apiBase := c.APIBase
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	ua := c.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}

	url := strings.TrimRight(apiBase, "/") + "/repos/" + c.Repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("update: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", ua)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: unexpected status %d from %s", resp.StatusCode, url)
	}

	var gr githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, fmt.Errorf("update: decode release JSON: %w", err)
	}

	rel := &Release{Tag: gr.TagName}
	for _, a := range gr.Assets {
		rel.Assets = append(rel.Assets, Asset{
			Name: a.Name,
			URL:  a.BrowserDownloadURL,
			Size: a.Size,
		})
	}
	return rel, nil
}

// Available returns the latest release if it is newer than current, else nil.
// Any network/parse error is returned (caller decides to log-and-ignore).
func (c Checker) Available(ctx context.Context, current string) (*Release, error) {
	rel, err := c.Latest(ctx)
	if err != nil {
		return nil, err
	}
	if !Newer(current, rel.Tag) {
		return nil, nil
	}
	return rel, nil
}

// Newer reports whether tag latest is a strictly higher semantic version than
// current. Both may carry a leading "v". If current is not a clean vX.Y.Z
// (e.g. "dev" or a git-describe SHA from a local build), Newer returns false — an
// unversioned local build must never nag about updates. If latest is malformed,
// returns false too (fail closed).
func Newer(current, latest string) bool {
	cur, ok := parseSemver(current)
	if !ok {
		return false
	}
	lat, ok := parseSemver(latest)
	if !ok {
		return false
	}
	for i := 0; i < 3; i++ {
		if lat[i] != cur[i] {
			return lat[i] > cur[i]
		}
	}
	return false
}

// parseSemver strips an optional leading "v" and splits into exactly three
// non-negative integer components (MAJOR.MINOR.PATCH). Any build/prerelease
// suffix or extra/missing component makes the version "not clean" → ok=false.
func parseSemver(s string) (v [3]int, ok bool) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		// strconv.Atoi rejects any non-digit content, so suffixes like
		// "-3-gabc123" or "8-rc1" fail cleanly here.
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
