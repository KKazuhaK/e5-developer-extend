// Package appsetup creates or updates the Entra app registration e5extend
// signs in with: the application, its service principal, the permissions, the
// administrator consent and, for the application identity, a client secret.
//
// All Graph calls go through an injectable base URL and HTTP client. Setup is
// idempotent and additive: it never removes permissions, redirect URIs or
// consent that exist, it only adds what is missing. A plan is computed first
// (Setup.Plan) so an interface can show what will happen before Setup.Apply
// does it.
//
// No value returned from this package, and no error message, contains a token
// or a client secret.
package appsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBase is the Microsoft Graph v1.0 endpoint.
const DefaultBase = "https://graph.microsoft.com/v1.0"

// GraphAppID is the application ID of the Microsoft Graph service principal.
const GraphAppID = "00000003-0000-0000-c000-000000000000"

// APIError is an error response from Graph.
type APIError struct {
	Op      string // what was being done, e.g. "create the application"
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("%s: Graph returned HTTP %d", e.Op, e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// notYetThere reports whether the error looks like replication lag: the object
// was just created and Graph does not see it everywhere yet.
//
// Creating the service principal of a new app answers 400 Request_BadRequest
// "The appId '…' of the service principal does not reference a valid
// application object" while the app is still replicating (seen on a real
// tenant), so that message counts too.
//
// The 400 about the appId is always lag. A 404 or "does not exist" only is when
// the request targets an object this run just created (justCreated); otherwise
// the object really is missing and waiting would only hide the problem.
func (e *APIError) notYetThere(justCreated bool) bool {
	msg := strings.ToLower(e.Message)
	if strings.Contains(msg, "does not reference a valid application object") {
		return true
	}
	if !justCreated {
		return false
	}
	return e.Status == http.StatusNotFound ||
		strings.Contains(msg, "does not exist") ||
		strings.EqualFold(e.Code, "Request_ResourceNotFound")
}

// alreadyExists reports whether a create failed because the object is there
// already (409, or Graph's 400 "... already exists").
func (e *APIError) alreadyExists() bool {
	if e.Status == http.StatusConflict {
		return true
	}
	return e.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(e.Message), "already exist")
}

// client is a minimal Graph client for the calls app setup needs.
type client struct {
	base  string
	http  *http.Client
	token string
}

func (c *client) url(path string) string {
	if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
		return path
	}
	return strings.TrimRight(c.base, "/") + path
}

// do sends a request and decodes a JSON answer into out (when not nil).
func (c *client) do(ctx context.Context, op, method, path string, in, out any) error {
	return c.send(ctx, op, method, path, in, out, true)
}

// get reads one object without the eventual-consistency header, so the answer
// is the object as it is now, not a replica's older copy.
func (c *client) get(ctx context.Context, op, path string, out any) error {
	return c.send(ctx, op, http.MethodGet, path, nil, out, false)
}

func (c *client) send(ctx context.Context, op, method, path string, in, out any, eventual bool) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), body)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if eventual {
		// Needed for filters on collections such as tags; harmless elsewhere.
		req.Header.Set("ConsistencyLevel", "eventual")
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error prints the URL, which holds no secret; the token is only in a header.
		return fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		ae := &APIError{Op: op, Status: resp.StatusCode}
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil {
			ae.Code, ae.Message = e.Error.Code, oneLine(e.Error.Message)
		}
		return ae
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s: unreadable answer: %w", op, err)
		}
	}
	return nil
}

// list follows @odata.nextLink and returns every item of a collection.
func (c *client) list(ctx context.Context, op, path string, each func(json.RawMessage) error) error {
	next := path
	for pages := 0; next != "" && pages < 1000; pages++ {
		var page struct {
			Value []json.RawMessage `json:"value"`
			Next  string            `json:"@odata.nextLink"`
		}
		if err := c.do(ctx, op, http.MethodGet, next, nil, &page); err != nil {
			return err
		}
		for _, v := range page.Value {
			if err := each(v); err != nil {
				return err
			}
		}
		next = page.Next
	}
	return nil
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

func filterQuery(filter string, extra ...string) string {
	q := url.Values{}
	q.Set("$filter", filter)
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	// Graph is happiest with %20 for spaces.
	return "?" + strings.ReplaceAll(q.Encode(), "+", "%20")
}

// retryReplication runs fn, and while it fails because Graph does not see a
// just-created object yet, waits and tries again for about two minutes.
//
// justCreated says the request targets an object this run created; only then
// is a 404 treated as lag (see APIError.notYetThere).
func (s *Setup) retryReplication(ctx context.Context, what string, justCreated bool, fn func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		var ae *APIError
		if !errors.As(err, &ae) || !ae.notYetThere(justCreated) || attempt >= len(s.backoff()) {
			return err
		}
		wait := s.backoff()[attempt]
		s.progress("waiting for Microsoft to replicate the new app (%s), retrying in %s...", what, wait.Round(time.Second))
		if err := s.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// DefaultBackoff adds up to about two minutes.
var DefaultBackoff = []time.Duration{
	2 * time.Second, 3 * time.Second, 5 * time.Second, 5 * time.Second, 8 * time.Second, 10 * time.Second,
	10 * time.Second, 15 * time.Second, 15 * time.Second, 20 * time.Second, 20 * time.Second,
}

func (s *Setup) backoff() []time.Duration {
	if s.Backoff != nil {
		return s.Backoff
	}
	return DefaultBackoff
}

func (s *Setup) sleep(ctx context.Context, d time.Duration) error {
	if s.Sleep != nil {
		return s.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Setup) progress(format string, args ...any) {
	if s.Progress != nil {
		s.Progress(fmt.Sprintf(format, args...))
	}
}
