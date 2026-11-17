package ops

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/recommend"
)

func TestRecommendCategories(t *testing.T) {
	e := newTestEnv(t)
	e.addDevice(t, "main")
	e.fa.tok.Scopes = catalog.RecommendedDelegated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer TOKEN-SECRET" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Path == "/me/mailboxSettings" {
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	e.GraphBase = srv.URL

	rec, err := e.RecommendCategories(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, c := range rec.Categories {
		status[c.Category] = c.Status
	}
	if status["mail"] != recommend.Unavailable || status["files"] != recommend.Recommended || status["user"] != recommend.Recommended {
		t.Errorf("statuses: %v", status)
	}
	if slices.Contains(rec.Recommended(), "mail") {
		t.Errorf("recommended %v", rec.Recommended())
	}

	if _, err := e.RecommendCategories(context.Background(), "nobody"); err == nil {
		t.Error("unknown account must fail")
	}
}

func TestRecommendCategoriesNeedsLogin(t *testing.T) {
	e := newTestEnv(t)
	e.addDevice(t, "main")
	e.fa.err = auth.ErrLoginRequired
	_, err := e.RecommendCategories(context.Background(), "main")
	if !errors.Is(err, auth.ErrLoginRequired) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetCategories(t *testing.T) {
	e := newTestEnv(t)
	e.addDevice(t, "main")
	ctx := context.Background()
	if err := e.SetCategories(ctx, "main", []string{"user", "files"}); err != nil {
		t.Fatal(err)
	}
	in, err := e.GetAccount("main")
	if err != nil || !slices.Equal(in.Categories, []string{"user", "files"}) || in.ClientID != testGUID {
		t.Fatalf("%+v %v", in, err)
	}
	if err := e.SetCategories(ctx, "main", nil); err == nil {
		t.Error("empty list must be refused")
	}
	if err := e.SetCategories(ctx, "main", []string{"bogus"}); err == nil {
		t.Error("unknown category must be refused")
	}
}

// SetCategories changes only the categories, under the config lock: edits made
// meanwhile to other accounts and fields survive.
func TestSetCategoriesKeepsConcurrentChanges(t *testing.T) {
	e := newTestEnv(t)
	e.addDevice(t, "a")
	e.addDevice(t, "b")
	ctx := context.Background()
	done := make(chan error, 2)
	go func() {
		var err error
		for i := 0; i < 15 && err == nil; i++ {
			err = e.SetCategories(ctx, "a", []string{"user", "mail"})
			if err == nil {
				err = e.SetCategories(ctx, "a", []string{"user"})
			}
		}
		done <- err
	}()
	go func() {
		var err error
		for i := 0; i < 15 && err == nil; i++ {
			_, err = e.Pause(ctx, "b")
			if err == nil {
				_, err = e.Resume(ctx, "b")
			}
			if err == nil {
				err = e.SetCategories(ctx, "b", []string{"files"})
			}
		}
		done <- err
	}()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	a, _ := e.GetAccount("a")
	b, _ := e.GetAccount("b")
	if !slices.Equal(a.Categories, []string{"user"}) || !slices.Equal(b.Categories, []string{"files"}) {
		t.Fatalf("a=%v b=%v", a.Categories, b.Categories)
	}
	if err := e.SetCategories(ctx, "ghost", []string{"user"}); err == nil {
		t.Error("unknown account must fail")
	}
}

func TestDefaultCategoriesExcludeReports(t *testing.T) {
	e := newTestEnv(t)
	d, err := e.DefaultCategories()
	if err != nil || slices.Contains(d, "reports") || !slices.Contains(d, "mail") {
		t.Fatalf("%v %v", d, err)
	}
}

func TestApplyCategoriesFreezesOnlyWhenDifferentFromDefaults(t *testing.T) {
	e := newTestEnv(t)
	e.addDevice(t, "main")
	ctx := context.Background()
	defaults, err := e.DefaultCategories()
	if err != nil {
		t.Fatal(err)
	}
	// An explicit list that is not the defaults is written as it is.
	explicit, err := e.ApplyCategories(ctx, "main", []string{"user", "files"})
	if err != nil || !explicit {
		t.Fatalf("explicit=%v err=%v", explicit, err)
	}
	if in, _ := e.GetAccount("main"); !slices.Equal(in.Categories, []string{"user", "files"}) {
		t.Fatalf("categories %v", in.Categories)
	}
	// Exactly the defaults (any order) leaves the list empty, so the account follows the catalog.
	rev := slices.Clone(defaults)
	slices.Reverse(rev)
	explicit, err = e.ApplyCategories(ctx, "main", rev)
	if err != nil || explicit {
		t.Fatalf("explicit=%v err=%v", explicit, err)
	}
	if in, _ := e.GetAccount("main"); len(in.Categories) != 0 {
		t.Fatalf("categories %v, want empty", in.Categories)
	}
	if _, err := e.ApplyCategories(ctx, "main", nil); err == nil {
		t.Error("empty list must be refused")
	}
	if _, err := e.ApplyCategories(ctx, "ghost", defaults); err == nil {
		t.Error("unknown account must fail")
	}
}
