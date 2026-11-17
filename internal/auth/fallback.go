package auth

import (
	"context"
	"time"
)

// MinPasswordInterval is the least time between two automatic password
// sign-ins of one account.
const MinPasswordInterval = 30 * time.Minute

// credentialFailureCodes are the errors of a password sign-in that retrying
// with the same password cannot fix: wrong or expired password, locked or
// disabled account, MFA or a Conditional Access block, missing consent. After
// one of them automatic password sign-in stops until the person acts, because
// repeating it would lock the account out.
var credentialFailureCodes = []string{
	"AADSTS50126", // wrong user name or password
	"AADSTS50053", // account locked (Smart Lockout)
	"AADSTS50055", // password expired
	"AADSTS50057", // account disabled
	"AADSTS50076", // MFA required
	"AADSTS50079", // MFA enrollment required
	"AADSTS53003", // blocked by Conditional Access
	"AADSTS65001", // consent required
}

// CredentialFailure returns the AADSTS code of err when it is one that stops
// automatic password sign-in, or "".
func CredentialFailure(err error) string {
	if err == nil {
		return ""
	}
	code := aadCode(err.Error())
	for _, c := range credentialFailureCodes {
		if c == code {
			return code
		}
	}
	return ""
}

// PasswordGate decides whether the automatic password sign-in of one account
// may run, and remembers what happened. The engine implements it over
// state.json, so the decision is shared by every run of the program.
type PasswordGate interface {
	// Allow reports whether a password sign-in may run now and, if so, records
	// the attempt. blocked is the code of the failure that stopped it, when
	// that is why it may not.
	Allow() (ok bool, blocked string)
	// Failed records a credential failure, which blocks further attempts.
	Failed(code string)
}

type gateKey struct{}

// AllowPasswordFallback lets Token sign a password account in again with its
// stored password when the silent refresh fails, as the gate permits. Without
// it Token only ever refreshes silently, which is what every caller except
// the background engine wants.
func AllowPasswordFallback(ctx context.Context, g PasswordGate) context.Context {
	return context.WithValue(ctx, gateKey{}, g)
}

// FallbackGate returns the gate AllowPasswordFallback put in ctx, or nil.
func FallbackGate(ctx context.Context) PasswordGate {
	g, _ := ctx.Value(gateKey{}).(PasswordGate)
	return g
}
