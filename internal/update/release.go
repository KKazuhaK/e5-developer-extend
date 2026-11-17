package update

import (
	"encoding/json"
	"fmt"
	"time"
)

// Repo is the GitHub repository releases come from.
const Repo = "KKazuhaK/e5-developer-extend"

// Update channels of config.toml's [update] section.
const (
	ChannelStable     = "stable"
	ChannelPrerelease = "prerelease"
)

// Asset is one file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release is the part of a GitHub release this package uses.
type Release struct {
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	URL         string    `json:"html_url"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Version parses the tag.
func (r Release) Version() (Version, error) { return ParseVersion(r.Tag) }

// Asset returns the asset with this exact name.
func (r Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// ParseRelease decodes the answer of GET /releases/latest.
func ParseRelease(b []byte) (Release, error) {
	var r Release
	if err := json.Unmarshal(b, &r); err != nil {
		return Release{}, fmt.Errorf("release: %w", err)
	}
	if r.Tag == "" {
		return Release{}, fmt.Errorf("release: no tag_name")
	}
	return r, nil
}

// ParseReleases decodes the answer of GET /releases.
func ParseReleases(b []byte) ([]Release, error) {
	var rs []Release
	if err := json.Unmarshal(b, &rs); err != nil {
		return nil, fmt.Errorf("releases: %w", err)
	}
	return rs, nil
}

// ValidChannel reports whether c is a known channel.
func ValidChannel(c string) bool { return c == ChannelStable || c == ChannelPrerelease }

// PickLatest returns the newest release a channel offers: drafts are never
// offered, pre-releases only on the "prerelease" channel. Tags that are not
// versions are ignored.
func PickLatest(rs []Release, channel string) (Release, bool) {
	var best Release
	var bestV Version
	found := false
	for _, r := range rs {
		v, err := r.Version()
		if err != nil || r.Draft {
			continue
		}
		if channel != ChannelPrerelease && (r.Prerelease || v.IsPrerelease()) {
			continue
		}
		if !found || v.Compare(bestV) > 0 {
			best, bestV, found = r, v, true
		}
	}
	return best, found
}
