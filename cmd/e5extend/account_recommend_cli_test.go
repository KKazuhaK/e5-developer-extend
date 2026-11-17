package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
)

// recommendEnv signs an account "main" in with the recommended permissions
// against a fake Graph in which the mailbox is missing.
func recommendEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	fa := &fakeAuth{tok: auth.Token{
		AccessToken: "SUPER-SECRET-TOKEN", HomeAccountID: "uid.tid", Account: "me@contoso.com",
		ExpiresOn: time.Now().Add(time.Hour), Scopes: catalog.RecommendedDelegated,
	}}
	e.newAuth = func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return fa, nil }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("non-GET probe %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Path == "/me/mailboxSettings" {
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	e.graphBase = srv.URL
	e.mustRun("", "account", "add", "main", "--auth", "device_code", "--tenant", "t.example", "--client-id", testGUID)
	return e
}

func TestAccountTestRecommendTable(t *testing.T) {
	e := recommendEnv(t)
	out := e.mustRun("", "account", "test", "main", "--recommend")
	for _, want := range []string{"CATEGORY", "STATUS", "REASON", "no Exchange mailbox", "suggested: categories = ["} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	var mailLine, filesLine string
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "mail "):
			mailLine = l
		case strings.HasPrefix(l, "files "):
			filesLine = l
		}
	}
	if !strings.Contains(mailLine, "unavailable") || !strings.Contains(filesLine, "recommended") {
		t.Errorf("mail %q files %q", mailLine, filesLine)
	}
	sugg := out[strings.Index(out, "suggested:"):]
	if strings.Contains(strings.SplitN(sugg, "\n", 2)[0], `"mail"`) || !strings.Contains(sugg, `"files"`) {
		t.Errorf("suggestion: %s", sugg)
	}
	if strings.Contains(out, "SUPER-SECRET-TOKEN") {
		t.Fatal("token printed")
	}
	// Without --apply the account is untouched.
	if got := e.loadCfg().Accounts[0].Categories; len(got) != 0 {
		t.Errorf("categories changed: %v", got)
	}
}

func TestAccountTestApply(t *testing.T) {
	e := recommendEnv(t)
	out := e.mustRun("", "account", "test", "main", "--recommend", "--apply", "--yes")
	if !strings.Contains(out, "saved the categories") {
		t.Fatalf("out: %s", out)
	}
	got := e.loadCfg().Accounts[0].Categories
	if len(got) == 0 || slices.Contains(got, "mail") || !slices.Contains(got, "files") || slices.Contains(got, "reports") {
		t.Fatalf("categories: %v", got)
	}
	// The second run has nothing left to change.
	out = e.mustRun("", "account", "test", "main", "--recommend", "--apply", "--yes")
	if !strings.Contains(out, "nothing to change") {
		t.Errorf("out: %s", out)
	}
}

func TestAccountTestApplyAsksFirst(t *testing.T) {
	e := recommendEnv(t)
	_, _, err := e.run("n\n", "account", "test", "main", "--recommend", "--apply")
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("err = %v", err)
	}
	if got := e.loadCfg().Accounts[0].Categories; len(got) != 0 {
		t.Fatalf("saved despite no: %v", got)
	}
	if _, _, err := e.run("", "account", "test", "main", "--recommend", "--apply"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("no answer: %v", err)
	}
	out, errOut, err := e.run("y\n", "account", "test", "main", "--recommend", "--apply")
	if err != nil || !strings.Contains(out, "saved the categories") || !strings.Contains(errOut, "Save these categories?") {
		t.Fatalf("out %q err %q %v", out, errOut, err)
	}
	if len(e.loadCfg().Accounts[0].Categories) == 0 {
		t.Fatal("not saved after yes")
	}
}

func TestAccountTestApplyNeedsRecommend(t *testing.T) {
	e := recommendEnv(t)
	for _, args := range [][]string{{"--apply"}, {"--yes"}, {"--recommend", "--yes"}} {
		if _, _, err := e.run("", append([]string{"account", "test", "main"}, args...)...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestAccountTestApplyNothingRecommended(t *testing.T) {
	e := recommendEnv(t)
	fa := &fakeAuth{tok: auth.Token{AccessToken: "T", Account: "x", ExpiresOn: time.Now().Add(time.Hour)}}
	e.newAuth = func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return fa, nil }
	_, _, err := e.run("", "account", "test", "main", "--recommend", "--apply", "--yes")
	if err == nil || !strings.Contains(err.Error(), "no category is recommended") {
		t.Fatalf("err = %v", err)
	}
	if len(e.loadCfg().Accounts[0].Categories) != 0 {
		t.Fatal("saved an empty recommendation")
	}
}

func TestAccountTestRecommendNotLoggedIn(t *testing.T) {
	e := newEnv(t)
	e.mustRun("", "account", "add", "main", "--auth", "device_code", "--tenant", "t.example", "--client-id", testGUID)
	_, _, err := e.run("", "account", "test", "main", "--recommend")
	if err == nil || !strings.Contains(err.Error(), "e5extend account login main") {
		t.Fatalf("err = %v", err)
	}
}

// Probes that fail temporarily must not cost the account any category.
func TestAccountTestApplyAfterFailedProbe(t *testing.T) {
	e := recommendEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/mailboxSettings":
			w.WriteHeader(404)
		case "/me/joinedTeams":
			w.WriteHeader(503)
		}
	}))
	t.Cleanup(srv.Close)
	e.graphBase = srv.URL
	if err := ops.New().SetCategories(context.Background(), "main", []string{"user", "mail", "teams"}); err != nil {
		t.Fatal(err)
	}

	_, _, err := e.run("", "account", "test", "main", "--recommend", "--apply", "--yes")
	if err == nil || !strings.Contains(err.Error(), "some probes failed temporarily; try again later") {
		t.Fatalf("err = %v", err)
	}
	if got := e.loadCfg().Accounts[0].Categories; !slices.Equal(got, []string{"user", "mail", "teams"}) {
		t.Fatalf("changed despite the refusal: %v", got)
	}

	// The suggestion line already keeps teams, which could not be checked.
	out := e.mustRun("", "account", "test", "main", "--recommend")
	line := out[strings.Index(out, "suggested:"):]
	line = strings.SplitN(line, "\n", 2)[0]
	if !strings.Contains(line, `"teams"`) || strings.Contains(line, `"mail"`) {
		t.Errorf("suggested: %s", line)
	}

	e.mustRun("", "account", "test", "main", "--recommend", "--apply", "--yes", "--force")
	got := e.loadCfg().Accounts[0].Categories
	if !slices.Contains(got, "teams") || slices.Contains(got, "mail") || !slices.Contains(got, "files") {
		t.Fatalf("after --force: %v", got)
	}
}

// Without a categories setting the catalog defaults run; an uncertain default stays selected.
func TestAccountTestApplyKeepsUncertainDefault(t *testing.T) {
	e := recommendEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/me/joinedTeams" {
			w.WriteHeader(503)
		}
	}))
	t.Cleanup(srv.Close)
	e.graphBase = srv.URL
	e.mustRun("", "account", "test", "main", "--recommend", "--apply", "--yes", "--force")
	if got := e.loadCfg().Accounts[0].Categories; !slices.Contains(got, "teams") || !slices.Contains(got, "mail") {
		t.Fatalf("%v", got)
	}
	if _, _, err := e.run("", "account", "test", "main", "--recommend", "--force"); err == nil {
		t.Error("--force without --apply accepted")
	}
}
