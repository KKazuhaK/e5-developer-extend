package ops

import (
	"context"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// saveHomeAccount stores the home account ID the engine got from a password
// sign-in. It writes only when the account on disk still has the identity the
// engine signed in with; if the account was edited meanwhile the ID belongs to
// a sign-in that no longer applies and is dropped.
func (o *Ops) saveHomeAccount(ctx context.Context, signedIn config.Account, homeID string) error {
	return o.updateAccount(ctx, signedIn.Name, func(ac *config.Account) {
		if ac.Auth == signedIn.Auth && ac.Tenant == signedIn.Tenant && ac.ClientID == signedIn.ClientID && ac.Username == signedIn.Username {
			ac.HomeAccountID = homeID
		}
	})
}
