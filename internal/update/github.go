package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Repo is the GitHub repository whose releases drive update detection.
const Repo = "get-vix/vix"

// latestReleaseURL is the GitHub REST endpoint returning the release marked
// "latest" (drafts and pre-releases excluded).
const latestReleaseURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// Release is the subset of a GitHub release we care about.
type Release struct {
	Tag  string // tag_name, e.g. "v1.4.0"
	URL  string // html_url to the release page
	Body string // release notes (markdown)
}

// LatestRelease fetches the latest published release from GitHub. It is
// deliberately best-effort: any network, rate-limit, or decode failure returns
// an error and callers are expected to silently skip the update check. The
// caller-supplied ctx should carry a short timeout.
func LatestRelease(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "vix-update-check")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain a bounded amount so the connection can be reused.
		io.CopyN(io.Discard, resp.Body, 4096)
		return Release{}, fmt.Errorf("github: unexpected status %d", resp.StatusCode)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return Release{}, err
	}
	if payload.TagName == "" {
		return Release{}, fmt.Errorf("github: empty tag_name")
	}
	return Release{Tag: payload.TagName, URL: payload.HTMLURL, Body: payload.Body}, nil
}
