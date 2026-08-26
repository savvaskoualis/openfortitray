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

// Newer reports whether tag latest is a strictly higher version than current.
// Both may carry a leading "v".
//
// The two sides are deliberately NOT treated alike:
//
//   - current MAY carry a prerelease suffix, and a clean release of the same
//     version supersedes it. So a hand-built "0.1.35-dev" is offered the real
//     0.1.35, and a git-describe build "v0.1.7-3-gabc123" is offered v0.1.8. This
//     is the point of the asymmetry: a dev build that can never see an update is a
//     dev build whose update path is never exercised until a user hits it.
//   - latest MUST be clean. A prerelease tag is never offered to anyone, however
//     high its version — pushing a release candidate at someone who asked for
//     stable is not an update, it is a surprise.
//
// A version with no numeric MAJOR.MINOR.PATCH core at all — a bare "dev", or the
// short SHA the Makefile stamps by default — still returns false. Running from
// source should not nag, and there is nothing to compare against anyway. If latest
// is malformed, this fails closed for the same reason it always did.
func Newer(current, latest string) bool {
	cur, curPre, ok := parseVersion(current)
	if !ok {
		return false
	}
	lat, latPre, ok := parseVersion(latest)
	if !ok || latPre != "" {
		return false
	}
	for i := 0; i < 3; i++ {
		if lat[i] != cur[i] {
			return lat[i] > cur[i]
		}
	}
	// Same core. The release supersedes a prerelease or dev build of itself, and
	// never supersedes an identical clean version.
	return curPre != ""
}

// parseVersion splits a version into its numeric MAJOR.MINOR.PATCH core and its
// prerelease suffix, if any.
//
// Build metadata after "+" is discarded: semver says it takes no part in
// precedence. The prerelease is everything after the FIRST "-", so
// "0.1.7-3-gabc123" is core 0.1.7 with prerelease "3-gabc123" rather than being
// rejected outright as it used to be.
//
// ok is false only when the core is not three non-negative integers, which is what
// makes "dev" and a bare SHA incomparable rather than merely suffixed.
func parseVersion(s string) (core [3]int, pre string, ok bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, pre = s[:i], s[i+1:]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return core, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return core, "", false
		}
		core[i] = n
	}
	return core, pre, true
}
