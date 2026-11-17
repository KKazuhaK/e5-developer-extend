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
