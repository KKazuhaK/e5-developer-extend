package graph

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestClassAndHeaders(t *testing.T) {
	var gotAuth, gotUA, gotCL, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA, gotCL = r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("ConsistencyLevel")
		gotQuery = r.URL.RawQuery
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(strings.Repeat("x", 2<<20)))
		case "/429":
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
		case "/503":
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(503)
		case "/503plain":
			w.WriteHeader(503)
		case "/401":
			w.WriteHeader(401)
		case "/403":
			w.WriteHeader(403)
		case "/404":
			w.WriteHeader(404)
		case "/400":
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL}

	tests := []struct {
		path  string
		class Class
		retry time.Duration
	}{
		{"/ok", OK, 0}, {"/429", Throttled, 7 * time.Second}, {"/503", Throttled, 3 * time.Second},
		{"/503plain", ServerError, 0}, {"/401", AuthFailed, 0}, {"/403", Forbidden, 0},
		{"/404", NotFound, 0}, {"/400", ClientError, 0},
	}
	for _, tc := range tests {
		r := c.Call(context.Background(), "tok", tc.path, url.Values{"$top": {"5"}, "$search": {"a"}})
		if r.Class != tc.class || r.RetryAfter != tc.retry {
			t.Errorf("%s: got %s retry %v", tc.path, r.Class, r.RetryAfter)
		}
	}
	if gotAuth != "Bearer tok" || !strings.HasPrefix(gotUA, "e5extend/") || gotCL != "eventual" {
		t.Errorf("headers: %q %q %q", gotAuth, gotUA, gotCL)
	}
	if !strings.Contains(gotQuery, "$top=5") {
		t.Errorf("query: %q", gotQuery)
	}
	c.Call(context.Background(), "tok", "/ok", url.Values{"$top": {"5"}})
	if gotCL != "" {
		t.Error("ConsistencyLevel sent without need")
	}
}

func TestNetworkAndCanceled(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := &Client{Base: url}
	r := c.Call(context.Background(), "secret-token", "/x", nil)
	if r.Class != Network || r.Err == nil {
		t.Fatalf("got %+v", r)
	}
	if strings.Contains(r.Err.Error(), "secret-token") {
		t.Error("token leaked in error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := c.Call(ctx, "t", "/x", nil); r.Class != Canceled {
		t.Errorf("got %s", r.Class)
	}
}

func TestRedirectDropsAuthOnOtherHost(t *testing.T) {
	var otherAuth, sameAuth string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherAuth = r.Header.Get("Authorization")
	}))
	defer other.Close()
	// "localhost" and "127.0.0.1" are different hosts for the redirect check.
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cross":
			http.Redirect(w, r, otherURL+"/csv", http.StatusFound)
		case "/same":
			http.Redirect(w, r, srv.URL+"/final", http.StatusFound)
		case "/final":
			sameAuth = r.Header.Get("Authorization")
		case "/loop":
			http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL}

	if r := c.Call(context.Background(), "tok", "/cross", nil); r.Class != OK {
		t.Fatalf("cross: %+v", r)
	}
	if otherAuth != "" {
		t.Errorf("Authorization sent to other host: %q", otherAuth)
	}
	if r := c.Call(context.Background(), "tok", "/same", nil); r.Class != OK || sameAuth != "Bearer tok" {
		t.Errorf("same host: %+v auth %q", r, sameAuth)
	}
	if r := c.Call(context.Background(), "tok", "/loop", nil); r.Class != Network {
		t.Errorf("loop: %+v", r)
	}
}

func TestListUserIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$top") != "50" {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"id":"u1"},{"id":"u2"},{"id":""}]}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL}
	ids, res := c.ListUserIDs(context.Background(), "t")
	if res.Class != OK || len(ids) != 2 || ids[1] != "u2" {
		t.Errorf("got %v %+v", ids, res)
	}
}

func TestParseRetryAfterClamps(t *testing.T) {
	cases := map[string]time.Duration{
		"": 0, "7": 7 * time.Second, "-5": 0, "99999999999": time.Hour,
		"99999999999999999999999999": time.Hour, "abc": 0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
	far := time.Now().Add(1000 * time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(far); got != time.Hour {
		t.Errorf("far date: %v", got)
	}
}
