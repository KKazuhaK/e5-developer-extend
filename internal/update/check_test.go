package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const latestJSON = `{
  "tag_name": "v0.3.0", "name": "E5 Developer Extend 0.3.0", "html_url": "https://github.com/KKazuhaK/e5-developer-extend/releases/tag/v0.3.0",
  "draft": false, "prerelease": false, "published_at": "2026-12-01T10:00:00Z", "body": "notes",
  "assets": [
    {"name": "SHA256SUMS", "browser_download_url": "https://github.com/x/SHA256SUMS", "size": 100},
    {"name": "e5extend_0.3.0_linux_amd64.tar.gz", "browser_download_url": "https://github.com/x/a.tar.gz", "size": 5}
  ]
}`

func TestParseRelease(t *testing.T) {
	r, err := ParseRelease([]byte(latestJSON))
	if err != nil {
		t.Fatal(err)
	}
	if r.Tag != "v0.3.0" || len(r.Assets) != 2 || r.PublishedAt.Year() != 2026 {
		t.Fatalf("%+v", r)
	}
	if a, ok := r.Asset("SHA256SUMS"); !ok || a.Size != 100 {
		t.Fatalf("asset: %+v %v", a, ok)
	}
	if _, ok := r.Asset("nope"); ok {
		t.Fatal("found a missing asset")
	}
	if _, err := ParseRelease([]byte(`{}`)); err == nil {
		t.Fatal("a release without a tag was accepted")
	}
	if _, err := ParseRelease([]byte(`not json`)); err == nil {
		t.Fatal("bad json was accepted")
	}
}

func TestPickLatest(t *testing.T) {
	rs := []Release{
		{Tag: "v0.2.0"}, {Tag: "v0.3.0-rc.1", Prerelease: true}, {Tag: "v0.2.5"},
		{Tag: "v0.9.0", Draft: true}, {Tag: "nightly"}, {Tag: "v0.3.0-rc.2"}, // an unmarked pre-release tag is still one
	}
	if r, ok := PickLatest(rs, ChannelStable); !ok || r.Tag != "v0.2.5" {
		t.Fatalf("stable: %+v %v", r, ok)
	}
	if r, ok := PickLatest(rs, ChannelPrerelease); !ok || r.Tag != "v0.3.0-rc.2" {
		t.Fatalf("prerelease: %+v %v", r, ok)
	}
	rs = append(rs, Release{Tag: "v0.3.0"})
	if r, _ := PickLatest(rs, ChannelPrerelease); r.Tag != "v0.3.0" {
		t.Fatalf("a release must beat its own release candidates, got %s", r.Tag)
	}
	if _, ok := PickLatest(nil, ChannelStable); ok {
		t.Fatal("found a release in nothing")
	}
}

func fakeGitHub(t *testing.T, h http.HandlerFunc) (*Checker, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Checker{Base: srv.URL, Version: "0.2.0"}, &n
}

func TestCheckStableUsesLatestEndpointAndETag(t *testing.T) {
	var gotPath, gotUA, gotAuth string
	var inm []string
	c, n := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotUA, gotAuth = r.URL.RequestURI(), r.Header.Get("User-Agent"), r.Header.Get("Authorization")
		inm = append(inm, r.Header.Get("If-None-Match"))
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		_, _ = w.Write([]byte(latestJSON))
	})
	now := time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC)
	cache, rel, err := c.Check(context.Background(), ChannelStable, Cache{}, now)
	if err != nil || rel == nil {
		t.Fatalf("first check: %v %v", rel, err)
	}
	if gotPath != "/repos/KKazuhaK/e5-developer-extend/releases/latest" || gotAuth != "" || !strings.HasPrefix(gotUA, "e5extend/") {
		t.Fatalf("request: %s UA=%q auth=%q", gotPath, gotUA, gotAuth)
	}
	if cache.Latest == nil || cache.Latest.Version != "0.3.0" || cache.ETag != `"abc"` || !cache.CheckedAt.Equal(now) {
		t.Fatalf("cache: %+v", cache)
	}
	if !cache.Available("0.2.0") || cache.Available("0.3.0") {
		t.Fatal("Available is wrong")
	}
	// The second check sends the ETag and keeps the answer on a 304.
	later := now.Add(25 * time.Hour)
	cache2, rel2, err := c.Check(context.Background(), ChannelStable, cache, later)
	if err != nil || rel2 != nil {
		t.Fatalf("second check: %v %v", rel2, err)
	}
	if inm[1] != `"abc"` || cache2.Latest == nil || cache2.Latest.Version != "0.3.0" || !cache2.CheckedAt.Equal(later) {
		t.Fatalf("304 handling: inm=%v cache=%+v", inm, cache2)
	}
	if n.Load() != 2 {
		t.Fatalf("%d requests", n.Load())
	}
}

func TestCheckPrereleaseChannelUsesList(t *testing.T) {
	var gotPath string
	c, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		_, _ = w.Write([]byte(`[{"tag_name":"v0.4.0-rc.1","prerelease":true,"html_url":"u"},{"tag_name":"v0.3.0","html_url":"u2"}]`))
	})
	cache, _, err := c.Check(context.Background(), ChannelPrerelease, Cache{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotPath, "/repos/KKazuhaK/e5-developer-extend/releases?per_page=") {
		t.Fatalf("path %s", gotPath)
	}
	if cache.Latest.Version != "0.4.0-rc.1" || !cache.Latest.Prerelease {
		t.Fatalf("%+v", cache.Latest)
	}
}

func TestCheckStableIgnoresPrereleaseAnswer(t *testing.T) {
	c, _ := fakeGitHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.4.0-rc.1","prerelease":true}`))
	})
	cache, rel, err := c.Check(context.Background(), ChannelStable, Cache{}, time.Now())
	if err != nil || rel != nil || cache.Latest != nil {
		t.Fatalf("a pre-release was offered on the stable channel: %+v %v %v", cache.Latest, rel, err)
	}
}

func TestCheckStableIgnoresPrereleaseTag(t *testing.T) {
	// GitHub's flag is not set, but the tag has a pre-release part.
	c, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_, _ = w.Write([]byte(`[{"tag_name":"v0.4.0-rc.1"}]`)) // nothing proper to fall back to
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.4.0-rc.1","prerelease":false}`))
	})
	cache, rel, err := c.Check(context.Background(), ChannelStable, Cache{}, time.Now())
	if err != nil || rel != nil || cache.Latest != nil {
		t.Fatalf("a pre-release tag was offered on the stable channel: %+v %v %v", cache.Latest, rel, err)
	}
}

func TestCheckStableFallsBackToProperStable(t *testing.T) {
	// The newest release has a pre-release tag but no flag: the newest proper
	// stable release is offered instead.
	c, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_, _ = w.Write([]byte(`{"tag_name":"v0.4.0-rc.1","prerelease":false}`))
			return
		}
		_, _ = w.Write([]byte(`[{"tag_name":"v0.4.0-rc.1","html_url":"u"},{"tag_name":"v0.3.0","html_url":"u2"},{"tag_name":"v0.2.5","html_url":"u3"}]`))
	})
	cache, rel, err := c.Check(context.Background(), ChannelStable, Cache{}, time.Now())
	if err != nil || rel == nil || cache.Latest == nil || cache.Latest.Version != "0.3.0" {
		t.Fatalf("fallback: %+v %v %v", cache.Latest, rel, err)
	}
}

func TestCheckNoReleaseYet(t *testing.T) {
	c, _ := fakeGitHub(t, func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	cache, rel, err := c.Check(context.Background(), ChannelStable, Cache{}, time.Now())
	if err != nil || rel != nil || cache.Latest != nil || cache.Failures != 0 {
		t.Fatalf("404: %+v %v %v", cache, rel, err)
	}
}

func TestCheckRateLimit(t *testing.T) {
	reset := time.Now().Add(40 * time.Minute).Truncate(time.Second)
	c, _ := fakeGitHub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
	})
	now := time.Now()
	cache, _, err := c.Check(context.Background(), ChannelStable, Cache{}, now)
	var rl *RateLimitError
	if err == nil || !errors.As(err, &rl) || !rl.Until.Equal(reset) {
		t.Fatalf("err = %v", err)
	}
	if !cache.NotBefore.Equal(reset) || cache.Failures != 1 {
		t.Fatalf("cache: %+v", cache)
	}
	if cache.Due(now.Add(10*time.Minute), ChannelStable) {
		t.Fatal("due before the rate limit resets")
	}
	if !cache.Due(reset.Add(time.Hour), ChannelStable) {
		t.Fatal("not due after the rate limit and the backoff")
	}
}

func TestCheckServerErrorBacksOff(t *testing.T) {
	c, _ := fakeGitHub(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cache, _, err := c.Check(context.Background(), ChannelStable, Cache{}, now)
	if err == nil || cache.Failures != 1 {
		t.Fatalf("%v %+v", err, cache)
	}
	if cache.Due(now.Add(5*time.Minute), ChannelStable) || !cache.Due(now.Add(11*time.Minute), ChannelStable) {
		t.Fatal("first backoff should be 10 minutes")
	}
	for i := 0; i < 10; i++ {
		cache, _, _ = c.Check(context.Background(), ChannelStable, cache, now)
	}
	if got := backoff(cache.Failures); got != maxBackoff {
		t.Fatalf("backoff = %s", got)
	}
}

func TestCacheDueDaily(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := Cache{Channel: ChannelStable, CheckedAt: now, Latest: &Latest{Version: "0.3.0"}}
	if c.Due(now.Add(23*time.Hour), ChannelStable) || !c.Due(now.Add(24*time.Hour), ChannelStable) {
		t.Fatal("daily rhythm")
	}
	if !c.Due(now, ChannelPrerelease) {
		t.Fatal("a channel change must check again")
	}
	if !(Cache{}).Due(now, ChannelStable) {
		t.Fatal("an empty cache is due")
	}
	if !c.Next(ChannelStable).Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("next = %s", c.Next(ChannelStable))
	}
}

func TestServiceCheckOfflineSafeAndCached(t *testing.T) {
	dir := t.TempDir()
	var fail atomic.Bool
	c, n := fakeGitHub(t, func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(latestJSON))
	})
	clock := time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC)
	s := &Service{DataDir: dir, Current: "0.2.0", Channel: ChannelStable, Checker: c, Now: func() time.Time { return clock }}
	if s.Cached().Available {
		t.Fatal("nothing is known yet")
	}
	info, err := s.Check(context.Background(), false)
	if err != nil || !info.Available || info.Latest.Version != "0.3.0" {
		t.Fatalf("%+v %v", info, err)
	}
	// Not due again for a day: answered from update.json without a request.
	if _, err := s.Check(context.Background(), false); err != nil || n.Load() != 1 {
		t.Fatalf("second check made %d requests (%v)", n.Load(), err)
	}
	// A new Service (another process) sees the same answer.
	s2 := &Service{DataDir: dir, Current: "0.2.0", Channel: ChannelStable, Now: s.Now}
	if !s2.Cached().Available {
		t.Fatal("update.json was not shared")
	}
	// Offline later: the error comes back together with the last known answer.
	clock = clock.Add(48 * time.Hour)
	fail.Store(true)
	info, err = s.Check(context.Background(), false)
	if err == nil || !info.Available {
		t.Fatalf("offline check: %+v %v", info, err)
	}
	// A forced check is made even when none is due.
	fail.Store(false)
	before := n.Load()
	if _, err := s.Check(context.Background(), true); err != nil || n.Load() != before+1 {
		t.Fatalf("forced check: %v requests %d->%d", err, before, n.Load())
	}
}

func TestServiceDevBuildNeverChecksOnItsOwn(t *testing.T) {
	c, n := fakeGitHub(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(latestJSON)) })
	s := &Service{DataDir: t.TempDir(), Current: "dev", Checker: c}
	info, err := s.Check(context.Background(), false)
	if err != nil || n.Load() != 0 || info.Available || !info.Dev {
		t.Fatalf("%+v %v requests=%d", info, err, n.Load())
	}
	info, err = s.Check(context.Background(), true)
	if err != nil || n.Load() != 1 || info.Available || info.Latest == nil {
		t.Fatalf("forced: %+v %v", info, err)
	}
}

func TestServiceReleaseIsFetchedFresh(t *testing.T) {
	var inm string
	c, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		inm = r.Header.Get("If-None-Match")
		_, _ = w.Write([]byte(latestJSON))
	})
	s := &Service{DataDir: t.TempDir(), Current: "0.2.0", Checker: c}
	_ = SaveCache(s.DataDir, Cache{Channel: ChannelStable, ETag: `"x"`, Latest: &Latest{Version: "0.3.0"}})
	rel, err := s.Release(context.Background())
	if err != nil || rel.Tag != "v0.3.0" || inm != "" {
		t.Fatalf("%+v %v inm=%q", rel, err, inm)
	}
}
