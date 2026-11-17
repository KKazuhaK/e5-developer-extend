package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIBase is the GitHub REST API.
const DefaultAPIBase = "https://api.github.com"

const (
	// CheckEvery is how often a check runs on its own.
	CheckEvery = 24 * time.Hour
	// maxAPIBody bounds what is read from the API; a page of 15 releases is far below it.
	maxAPIBody = 8 << 20
	// maxBackoff caps the wait after failed checks.
	maxBackoff = 6 * time.Hour
	listPage   = 15
)

// RateLimitError means GitHub refused the request because the unauthenticated
// rate limit is used up. Until is when the limit resets (zero when unknown).
type RateLimitError struct{ Until time.Time }

func (e *RateLimitError) Error() string {
	if e.Until.IsZero() {
		return "GitHub rate limit reached; try again later"
	}
	return "GitHub rate limit reached; try again after " + e.Until.Local().Format("15:04")
}

// Checker asks GitHub for releases. No credentials are sent: releases of a
// public repository need none.
type Checker struct {
	// HTTP is the client to use (it carries the configured proxy); a client
	// with a 20-second timeout when nil.
	HTTP *http.Client
	// Base is the API address; DefaultAPIBase when empty. Tests point it at a fake server.
	Base string
	// Version is the running version, for the User-Agent.
	Version string
}

func (c *Checker) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *Checker) base() string {
	if c.Base != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return DefaultAPIBase
}

// endpoint is the API path of a channel: the stable channel asks for the
// latest release, which GitHub never fills with a pre-release or a draft; the
// pre-release channel reads the newest releases and picks one itself.
func endpoint(channel string) string {
	if channel == ChannelPrerelease {
		return "/repos/" + Repo + "/releases?per_page=" + strconv.Itoa(listPage)
	}
	return "/repos/" + Repo + "/releases/latest"
}

// Latest is the newest release a check saw.
type Latest struct {
	Tag        string    `json:"tag"`
	Version    string    `json:"version"`
	URL        string    `json:"url"`
	Prerelease bool      `json:"prerelease,omitempty"`
	Published  time.Time `json:"published,omitzero"`
}

func latestOf(r Release) (Latest, error) {
	v, err := r.Version()
	if err != nil {
		return Latest{}, err
	}
	return Latest{Tag: r.Tag, Version: v.String(), URL: r.URL, Prerelease: r.Prerelease || v.IsPrerelease(), Published: r.PublishedAt}, nil
}

// Cache is what update.json keeps between checks, so a check made a minute
// ago is not repeated and a 304 answer can stand in for the body.
type Cache struct {
	Version int    `json:"version"`
	Channel string `json:"channel,omitempty"`
	ETag    string `json:"etag,omitempty"`
	// CheckedAt is the last time GitHub answered; AttemptedAt the last try.
	CheckedAt   time.Time `json:"checked_at,omitzero"`
	AttemptedAt time.Time `json:"attempted_at,omitzero"`
	// NotBefore is the earliest next try after a rate limit.
	NotBefore time.Time `json:"not_before,omitzero"`
	// Failures counts the failed tries in a row, for the backoff.
	Failures int     `json:"failures,omitempty"`
	Latest   *Latest `json:"latest,omitempty"`
}

// CacheVersion is the current update.json format version.
const CacheVersion = 1

// Due reports whether a check is due at now for this channel.
func (c Cache) Due(now time.Time, channel string) bool {
	if now.Before(c.NotBefore) {
		return false
	}
	if c.Channel != channel || c.CheckedAt.IsZero() && c.Failures == 0 {
		return true
	}
	if c.Failures > 0 {
		return !now.Before(c.AttemptedAt.Add(backoff(c.Failures)))
	}
	return !now.Before(c.CheckedAt.Add(CheckEvery))
}

// Next is when the next check on its own is due; the zero time means now.
func (c Cache) Next(channel string) time.Time {
	var t time.Time
	switch {
	case c.Channel != channel || c.CheckedAt.IsZero() && c.Failures == 0:
	case c.Failures > 0:
		t = c.AttemptedAt.Add(backoff(c.Failures))
	default:
		t = c.CheckedAt.Add(CheckEvery)
	}
	if c.NotBefore.After(t) {
		t = c.NotBefore
	}
	return t
}

// backoff is the wait after n failed tries in a row: 10 minutes, doubling, at most 6 hours.
func backoff(n int) time.Duration {
	d := 10 * time.Minute
	for i := 1; i < n && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

// Fetch asks for the release a channel offers. A nil release without an
// error means there is none yet.
func (c *Checker) Fetch(ctx context.Context, channel string) (*Release, error) {
	rel, _, _, err := c.get(ctx, channel, "")
	return rel, err
}

// Check is Fetch with caching: it sends the ETag of the last answer and keeps
// what it learns in the returned cache (also on failure, where only the
// failure bookkeeping changes). release is nil on a 304 and when nothing is
// published; the answer is then in the cache.
func (c *Checker) Check(ctx context.Context, channel string, prev Cache, now time.Time) (Cache, *Release, error) {
	next := prev
	next.Version = CacheVersion
	if prev.Channel != channel {
		next = Cache{Version: CacheVersion, Channel: channel, AttemptedAt: prev.AttemptedAt, NotBefore: prev.NotBefore, Failures: prev.Failures}
	}
	next.AttemptedAt = now
	etag := next.ETag
	if next.Latest == nil {
		etag = "" // nothing to fall back on if the answer is "not modified"
	}
	rel, newETag, notModified, err := c.get(ctx, channel, etag)
	if err != nil {
		next.Failures++
		var rl *RateLimitError
		if errors.As(err, &rl) && !rl.Until.IsZero() {
			next.NotBefore = rl.Until
		}
		return next, nil, err
	}
	next.Failures, next.NotBefore = 0, time.Time{}
	next.CheckedAt = now
	next.ETag = newETag
	if notModified {
		return next, nil, nil
	}
	if rel == nil {
		next.Latest = nil
		return next, nil, nil
	}
	l, err := latestOf(*rel)
	if err != nil {
		next.Failures++
		return next, nil, fmt.Errorf("the newest release has a tag that is not a version: %w", err)
	}
	next.Latest = &l
	return next, rel, nil
}

// get makes the request. For the list endpoint the newest release of the
// channel is picked here.
func (c *Checker) get(ctx context.Context, channel, etag string) (rel *Release, newETag string, notModified bool, err error) {
	body, newETag, notModified, found, err := c.fetch(ctx, endpoint(channel), etag, channel != ChannelPrerelease)
	if err != nil || notModified || !found {
		return nil, newETag, notModified, err
	}
	if channel == ChannelPrerelease {
		rs, err := ParseReleases(body)
		if err != nil {
			return nil, "", false, err
		}
		r, ok := PickLatest(rs, channel)
		if !ok {
			return nil, newETag, false, nil
		}
		return &r, newETag, false, nil
	}
	r, err := ParseRelease(body)
	if err != nil {
		return nil, "", false, err
	}
	if v, verr := r.Version(); r.Draft || r.Prerelease || (verr == nil && v.IsPrerelease()) {
		if r.Draft || r.Prerelease {
			return nil, newETag, false, nil // never offered on the stable channel
		}
		// The newest release has a pre-release tag although GitHub does not
		// flag it: offer the newest proper stable release instead.
		list, _, _, ok, lerr := c.fetch(ctx, endpoint(ChannelPrerelease), "", false)
		if lerr != nil || !ok {
			return nil, newETag, false, lerr
		}
		rs, perr := ParseReleases(list)
		if perr != nil {
			return nil, "", false, perr
		}
		best, ok := PickLatest(rs, ChannelStable)
		if !ok {
			return nil, newETag, false, nil
		}
		return &best, newETag, false, nil
	}
	return &r, newETag, false, nil
}

// fetch requests path and returns the body. found is false for a 404 when
// missingOK is set (no release has been published yet).
func (c *Checker) fetch(ctx context.Context, path, etag string, missingOK bool) (body []byte, newETag string, notModified, found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
	if err != nil {
		return nil, "", false, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "e5extend/"+orDev(c.Version))
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, "", false, false, fmt.Errorf("ask GitHub for the latest release: %w", urlErrorText(err))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return nil, resp.Header.Get("ETag"), true, false, nil
	case resp.StatusCode == http.StatusNotFound && missingOK:
		return nil, "", false, false, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden && rateLimited(resp):
		return nil, "", false, false, &RateLimitError{Until: resetTime(resp)}
	case resp.StatusCode != http.StatusOK:
		return nil, "", false, false, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxAPIBody))
	if err != nil {
		return nil, "", false, false, fmt.Errorf("read the answer of GitHub: %w", err)
	}
	return body, resp.Header.Get("ETag"), false, true, nil
}

func orDev(v string) string {
	if v == "" {
		return "dev"
	}
	return v
}

// urlErrorText drops the request address from a transport error: the proxy
// address of the configuration may be part of it and may carry a password.
func urlErrorText(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

func rateLimited(resp *http.Response) bool {
	return resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""
}

// resetTime reads when a rate limit ends from Retry-After or X-RateLimit-Reset.
func resetTime(resp *http.Response) time.Time {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 {
			return time.Now().Add(time.Duration(n) * time.Second)
		}
	}
	if s := resp.Header.Get("X-RateLimit-Reset"); s != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && n > 0 {
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

// Available reports whether the cache knows a release newer than current.
func (c Cache) Available(current string) bool {
	return c.Latest != nil && Newer(current, c.Latest.Version)
}

// MarshalCache encodes a cache for update.json.
func MarshalCache(c Cache) ([]byte, error) {
	c.Version = CacheVersion
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
