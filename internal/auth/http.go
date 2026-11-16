package auth

import (
	"net/http"
	"net/url"
)

// NewHTTPClient returns a client that uses proxy when set and the standard
// proxy environment variables otherwise. There is no global timeout; callers
// bound requests with a context.
func NewHTTPClient(proxy string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		if u, err := url.Parse(proxy); err == nil && u.Host != "" {
			tr.Proxy = http.ProxyURL(u)
		}
	} else {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Transport: tr}
}
