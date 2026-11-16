// Package graph makes single read-only Microsoft Graph calls and classifies
// the outcome. It never keeps response content.
package graph

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

	"github.com/KKazuhaK/e5-renew/internal/version"
)

// DefaultBase is the Graph v1.0 endpoint.
const DefaultBase = "https://graph.microsoft.com/v1.0"

const (
	maxRedirects = 5
	maxBody      = 1 << 20
	// Timeout bounds one request, including redirects.
	Timeout = 60 * time.Second
)

// Class is the outcome category of a call.
type Class string

// Outcome classes.
const (
	OK          Class = "ok"
	Throttled   Class = "throttled"
	AuthFailed  Class = "auth_failed"
	Forbidden   Class = "forbidden"
	NotFound    Class = "not_found"
	ServerError Class = "server_error"
	Network     Class = "network"
	ClientError Class = "client_error"
	Canceled    Class = "canceled"
)

// Result describes one call. Err is set for Network and Canceled only.
type Result struct {
	Status     int
	Latency    time.Duration
	Class      Class
	RetryAfter time.Duration
	Err        error
}

// Client calls Graph.
type Client struct {
	HTTP *http.Client
	Base string // defaults to DefaultBase
}

// Call sends GET base+path?query with the bearer token. The response body is
// read (up to 1 MB) and discarded.
func (c *Client) Call(ctx context.Context, token, path string, query url.Values) Result {
	return c.do(ctx, token, path, query, nil)
}

// ListUserIDs fetches a page of user IDs, used to pick the target of
// application-permission calls. The IDs are returned to the caller only.
func (c *Client) ListUserIDs(ctx context.Context, token string) ([]string, Result) {
	var ids []string
	res := c.do(ctx, token, "/users", url.Values{"$select": {"id"}, "$top": {"50"}}, func(r io.Reader) {
		var body struct {
			Value []struct {
				ID string `json:"id"`
			} `json:"value"`
		}
		if json.NewDecoder(r).Decode(&body) == nil {
			for _, u := range body.Value {
				if u.ID != "" {
					ids = append(ids, u.ID)
				}
			}
		}
	})
	if res.Class != OK {
		return nil, res
	}
	return ids, res
}

func (c *Client) do(ctx context.Context, token, path string, query url.Values, sink func(io.Reader)) Result {
	base := c.Base
	if base == "" {
		base = DefaultBase
	}
	target := strings.TrimRight(base, "/") + path
	if len(query) > 0 {
		// Graph accepts a literal $ in option names, which keeps URLs readable.
		target += "?" + strings.ReplaceAll(query.Encode(), "%24", "$")
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Result{Class: ClientError, Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "e5renew/"+version.Version)
	if needsConsistency(query) {
		req.Header.Set("ConsistencyLevel", "eventual")
	}

	hc := http.DefaultClient
	if c.HTTP != nil {
		hc = c.HTTP
	}
	cp := *hc
	// The standard client drops Authorization when a redirect leaves the host.
	cp.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return nil
	}

	start := time.Now()
	resp, err := cp.Do(req)
	if err != nil {
		res := Result{Latency: time.Since(start), Class: Network, Err: sanitize(err)}
		if parent := ctx.Err(); errors.Is(parent, context.Canceled) {
			res.Class = Canceled
		}
		return res
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, maxBody)
	if sink != nil && resp.StatusCode/100 == 2 {
		sink(body)
	}
	_, _ = io.Copy(io.Discard, body)

	res := Result{Status: resp.StatusCode, Latency: time.Since(start)}
	res.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	res.Class = classify(resp.StatusCode, resp.Header.Get("Retry-After") != "")
	return res
}

func classify(status int, hasRetryAfter bool) Class {
	switch {
	case status >= 200 && status < 300:
		return OK
	case status == 429, status == 503 && hasRetryAfter:
		return Throttled
	case status == 401:
		return AuthFailed
	case status == 403:
		return Forbidden
	case status == 404:
		return NotFound
	case status >= 500:
		return ServerError
	default:
		return ClientError
	}
}

func needsConsistency(q url.Values) bool {
	for k := range q {
		if k == "$search" || k == "$count" {
			return true
		}
	}
	return false
}

// maxRetryAfter bounds what a server can ask for; larger values would also
// overflow time.Duration.
const maxRetryAfter = time.Hour

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Duration(min(max(n, 0), int64(maxRetryAfter/time.Second))) * time.Second
	}
	if allDigits(v) { // too large for int64
		return maxRetryAfter
	}
	if t, err := http.ParseTime(v); err == nil {
		return min(max(time.Until(t), 0), maxRetryAfter)
	}
	return 0
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// sanitize drops the request URL from transport errors.
func sanitize(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
