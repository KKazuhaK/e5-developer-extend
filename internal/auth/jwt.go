package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

type claims struct {
	Scp   string   `json:"scp"`
	Roles []string `json:"roles"`
	AppID string   `json:"appid"`
	AZP   string   `json:"azp"`
}

// parseScopes returns the granted permissions from an access token's scp
// (delegated) or roles (application) claim. The signature is not verified;
// the token is only inspected for display and API selection.
func parseScopes(jwt string) []string {
	c, ok := parseClaims(jwt)
	if !ok {
		return nil
	}
	if c.Scp != "" {
		return strings.Fields(c.Scp)
	}
	return c.Roles
}

func parseClaims(jwt string) (claims, bool) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return claims{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return claims{}, false
	}
	var c claims
	if json.Unmarshal(raw, &c) != nil {
		return claims{}, false
	}
	return c, true
}
