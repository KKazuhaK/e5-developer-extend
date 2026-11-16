package auth

import "context"

type forceKey struct{}

// forceClaims makes MSAL skip its cached access token and ask the identity
// service for a new one. The claim itself carries no requirement.
const forceClaims = `{"access_token":{"nbf":{"essential":true}}}`

// ForceRefresh marks ctx so that Token ignores the cached access token. Use it
// after Graph rejected a token that still looked valid.
func ForceRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, forceKey{}, true)
}

// Forced reports whether ctx asks for a fresh token.
func Forced(ctx context.Context) bool {
	v, _ := ctx.Value(forceKey{}).(bool)
	return v
}
