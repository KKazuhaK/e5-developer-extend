package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	msalerrors "github.com/AzureAD/microsoft-authentication-library-for-go/apps/errors"
)

// Error classes for the scheduler and health checks; test with errors.Is.
var (
	// ErrLoginRequired means the user must sign in again.
	ErrLoginRequired = errors.New("login required")
	// ErrInvalidClient means the client secret or app registration is wrong or expired.
	ErrInvalidClient = errors.New("invalid client credentials")
	// ErrConsentRequired means an administrator must grant consent.
	ErrConsentRequired = errors.New("admin consent required")
	// ErrTransient means a retry may succeed (network, timeout, 5xx, 429).
	ErrTransient = errors.New("transient error")
)

// codeKinds maps AADSTS codes, taken from the parsed error response, to classes.
var codeKinds = map[string]error{
	"AADSTS65001":   ErrConsentRequired,
	"AADSTS7000215": ErrInvalidClient,
	"AADSTS7000222": ErrInvalidClient,
	"AADSTS700016":  ErrInvalidClient,
	"AADSTS50173":   ErrLoginRequired,
	"AADSTS700082":  ErrLoginRequired,
	"AADSTS50126":   ErrLoginRequired, // wrong user name or password
	"AADSTS50076":   ErrLoginRequired, // MFA required
	"AADSTS50079":   ErrLoginRequired, // MFA enrollment required
	"AADSTS50078":   ErrLoginRequired,
	"AADSTS70043":   ErrLoginRequired,
	"AADSTS50132":   ErrLoginRequired,
}

// nameKinds maps OAuth error names to classes; they are only consulted when no
// known AADSTS code matched, because AADSTS65001 and others also carry "invalid_grant".
var nameKinds = []struct {
	needle string
	kind   error
}{
	{"interaction_required", ErrLoginRequired},
	{"login_required", ErrLoginRequired},
	{"invalid_grant", ErrLoginRequired},
	{"no token found", ErrLoginRequired},
}

var aadstsRe = regexp.MustCompile(`AADSTS\d+[^"\\\r\n]*`)
var aadstsCodeRe = regexp.MustCompile(`AADSTS\d+`)

// aadCode extracts the AADSTS code from the JSON error response embedded in
// text, falling back to a pattern search.
func aadCode(text string) string {
	if a, b := strings.Index(text, "{"), strings.LastIndex(text, "}"); a >= 0 && b > a {
		var body struct {
			Description string `json:"error_description"`
			Codes       []int  `json:"error_codes"`
		}
		if json.Unmarshal([]byte(text[a:b+1]), &body) == nil {
			if len(body.Codes) > 0 {
				return fmt.Sprintf("AADSTS%d", body.Codes[0])
			}
			if c := aadstsCodeRe.FindString(body.Description); c != "" {
				return c
			}
		}
	}
	return aadstsCodeRe.FindString(text)
}

type classified struct {
	kind  error
	cause error
	msg   string
}

func (e *classified) Error() string {
	if e.msg == "" {
		return e.kind.Error()
	}
	return e.kind.Error() + ": " + e.msg
}

func (e *classified) Unwrap() []error { return []error{e.kind, e.cause} }

// Classify maps err onto one of the package's error classes. Errors that do
// not match any class are returned unchanged.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	for _, k := range []error{ErrLoginRequired, ErrInvalidClient, ErrConsentRequired, ErrTransient} {
		if errors.Is(err, k) {
			return err
		}
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	text := err.Error()

	var call msalerrors.CallErr
	if errors.As(err, &call) && call.Resp != nil {
		if sc := call.Resp.StatusCode; sc == 429 || sc >= 500 {
			return &classified{ErrTransient, err, fmt.Sprintf("HTTP %d from the identity service", sc)}
		}
	}
	if kind, ok := codeKinds[aadCode(text)]; ok {
		return &classified{kind, err, describe(text)}
	}
	for _, r := range nameKinds {
		if strings.Contains(text, r.needle) {
			return &classified{r.kind, err, describe(text)}
		}
	}

	var netErr net.Error
	var urlErr *url.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) || errors.As(err, &urlErr) {
		return &classified{ErrTransient, err, firstLine(text)}
	}
	return err
}

func describe(text string) string {
	if m := aadstsRe.FindString(text); m != "" {
		return strings.TrimSpace(m)
	}
	return firstLine(text)
}

// firstLine flattens s into one trimmed line of bounded length.
func firstLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// ErrorCode returns a short, content-free tag for err: an AADSTS code or an
// HTTP status from the identity service, or "" when there is none. Unlike the
// error text it never carries tenant names, user names or correlation IDs.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var call msalerrors.CallErr
	if errors.As(err, &call) && call.Resp != nil && call.Resp.StatusCode > 0 {
		if c := aadCode(err.Error()); c != "" {
			return c
		}
		return fmt.Sprintf("HTTP %d", call.Resp.StatusCode)
	}
	return aadCode(err.Error())
}

// SignInFrequencyLabel is the fixed wording for a refresh token that a
// Conditional Access sign-in frequency policy ended (AADSTS70043). The GUI
// recognizes the code in it to offer the way out.
const SignInFrequencyLabel = "sign-in is cut off by a Conditional Access sign-in frequency policy (AADSTS70043)"

// IsSignInFrequency reports whether err is the sign-in frequency cutoff.
func IsSignInFrequency(err error) bool {
	return err != nil && errors.Is(err, ErrLoginRequired) && aadCode(err.Error()) == "AADSTS70043"
}
