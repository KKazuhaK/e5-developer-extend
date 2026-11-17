package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func TestSignInFrequencyCutoffHasItsOwnReason(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.auths["main"].setErr(fmt.Errorf("%w: AADSTS70043: sign-in frequency checks by conditional access", auth.ErrLoginRequired))
	f.start()
	f.round("main")
	as := f.acct("main")
	if as.Status != store.StatusNeedsAttention || !strings.Contains(as.Reason, "AADSTS70043") {
		t.Fatalf("got %+v", as)
	}
}
