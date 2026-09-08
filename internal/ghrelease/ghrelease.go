// Package ghrelease resolves Placard CLI releases from the GitHub Releases
// API. The CLI's self-update reads it to find what to install; the server
// reads it to pin the version its /install.sh hands out.
//
// The repository publishes two tag namespaces from one release list: `vX.Y.Z`
// is the SERVER and `cli/vX.Y.Z` is the CLI. Everything here filters on the
// `cli/v` prefix, so a server release can never be handed to a CLI.
package ghrelease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Xm798/placard/internal/version"
)

// TagPrefix marks a CLI release. The version is the tag with this stripped.
const TagPrefix = "cli/v"

// APIURL lists the repository's releases, newest first. Overridable in tests.
var APIURL = "https://api.github.com/repos/Xm798/placard/releases"

const (
	// maxResponseSize caps one page of the API response (4 MB). A full page of
	// releases is a few hundred KB; this is a backstop against a misrouted
	// endpoint streaming forever.
	maxResponseSize = 4 << 20
	// perPage is the page size, GitHub's maximum.
	perPage = 100
	// maxPages bounds how far back a search walks. Server and CLI releases
	// share one list, so a long run of server releases can push the newest CLI
	// release off the first page — without paging, `placard update` would then
	// report that no CLI release exists at all.
	maxPages = 10
)

// Release is one published CLI release.
type Release struct {
	// Version is the tag with TagPrefix stripped: "1.2.3".
	Version string
	// Prerelease is GitHub's flag. Latest skips these.
	Prerelease bool
	// Assets maps an asset's file name to its download URL. Using the URL
	// GitHub reports, rather than building one, is what keeps this working
	// with a tag that carries a slash.
	Assets map[string]string
}

type apiRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// List returns the CLI releases on one page of the API, in its own order
// (newest first), plus whether that page was full — a partial page is the last
// one. Draft releases are dropped: they carry no downloadable assets.
func List(ctx context.Context, hc *http.Client, page int) (releases []Release, full bool, err error) {
	url := fmt.Sprintf("%s?per_page=%d&page=%d", APIURL, perPage, page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// GitHub answers 403 to a request with no User-Agent.
	req.Header.Set("User-Agent", "placard/"+version.Version)

	resp, err := hc.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("fetching %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", url, err)
	}
	var raw []apiRelease
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false, fmt.Errorf("parsing %s: %w", url, err)
	}

	out := make([]Release, 0, len(raw))
	for _, r := range raw {
		if r.Draft || !strings.HasPrefix(r.TagName, TagPrefix) {
			continue
		}
		rel := Release{
			Version:    strings.TrimPrefix(r.TagName, TagPrefix),
			Prerelease: r.Prerelease,
			Assets:     make(map[string]string, len(r.Assets)),
		}
		if !version.IsRelease(rel.Version) {
			continue
		}
		for _, a := range r.Assets {
			rel.Assets[a.Name] = a.URL
		}
		out = append(out, rel)
	}
	return out, len(raw) == perPage, nil
}

// search walks pages newest-first until match accepts a release or the
// releases run out.
func search(ctx context.Context, hc *http.Client, match func(Release) bool) (Release, error) {
	for page := 1; page <= maxPages; page++ {
		releases, full, err := List(ctx, hc, page)
		if err != nil {
			return Release{}, err
		}
		for _, r := range releases {
			if match(r) {
				return r, nil
			}
		}
		if !full {
			break
		}
	}
	return Release{}, errNotFound
}

// errNotFound is turned into a message naming what was looked for by the
// caller, which is the only one that knows.
var errNotFound = errors.New("not found")

// Latest returns the newest stable CLI release. Prereleases are skipped: an
// update must not walk a user onto a release candidate they did not ask for.
func Latest(ctx context.Context, hc *http.Client) (Release, error) {
	rel, err := search(ctx, hc, func(r Release) bool { return !r.Prerelease })
	if errors.Is(err, errNotFound) {
		return Release{}, fmt.Errorf("no %s* release published at %s", TagPrefix, APIURL)
	}
	return rel, err
}

// Find returns the release for an exact version, prerelease included — the
// caller has already decided which version it wants.
func Find(ctx context.Context, hc *http.Client, v string) (Release, error) {
	rel, err := search(ctx, hc, func(r Release) bool { return r.Version == v })
	if errors.Is(err, errNotFound) {
		return Release{}, fmt.Errorf("no release tagged %s%s at %s", TagPrefix, v, APIURL)
	}
	return rel, err
}
