package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

type hookRecv struct {
	mu   sync.Mutex
	got  []map[string]string
	path []string
	code int
}

func (h *hookRecv) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]string
	_ = json.Unmarshal(b, &m)
	h.mu.Lock()
	h.got = append(h.got, m)
	h.path = append(h.path, r.URL.Path)
	code := h.code
	h.mu.Unlock()
	if code != 0 {
		w.WriteHeader(code)
	}
}

func TestNotifySetTestClear(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	rc := &hookRecv{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	hookURL := srv.URL + "/hook/SECRETTOKEN123"

	if _, _, err := e.run("", "notify", "test"); err == nil || !strings.Contains(err.Error(), "no notification channel") {
		t.Fatalf("test without webhook: %v", err)
	}
	if _, _, err := e.run("", "notify", "set-webhook", "not a url"); err == nil || strings.Contains(err.Error(), "not a url") {
		t.Fatalf("bad address: %v", err)
	}

	// Legacy plaintext entry in the config is removed by set-webhook.
	e.mustRun("", "config", "init")
	cfgPath := filepath.Join(e.cfgDir, "config.toml")
	cfg := e.loadCfg()
	cfg.Notify.WebhookURL = hookURL
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	// The legacy address works for notify test before migration.
	out := e.mustRun("", "notify", "test")
	if !strings.Contains(out, "delivered") || len(rc.got) != 1 {
		t.Fatalf("legacy delivery: %q hits=%d", out, len(rc.got))
	}

	out = e.mustRun("", "notify", "set-webhook", hookURL)
	if strings.Contains(out, "SECRETTOKEN123") {
		t.Errorf("set-webhook printed the address: %s", out)
	}
	if !strings.Contains(out, "removed notify.webhook_url") || e.loadCfg().Notify.WebhookURL != "" {
		t.Errorf("config not cleaned: %s", out)
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "SECRETTOKEN123") {
		t.Error("address still in config.toml")
	}
	// The vault file does not hold the address in plain text.
	_ = filepath.Walk(e.dataDir, func(p string, fi os.FileInfo, _ error) error {
		if fi != nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "SECRETTOKEN123") {
				t.Errorf("address readable in %s", p)
			}
		}
		return nil
	})

	out = e.mustRun("", "notify", "test")
	if !strings.Contains(out, "delivered") || strings.Contains(out, "SECRETTOKEN123") {
		t.Errorf("test output: %s", out)
	}
	if len(rc.got) != 2 || rc.path[1] != "/hook/SECRETTOKEN123" || rc.got[1]["event"] != "test" || !strings.HasPrefix(rc.got[1]["text"], "[e5extend]") {
		t.Errorf("received: %+v", rc.got)
	}

	// A failing receiver: the error is clear and does not contain the address.
	rc.mu.Lock()
	rc.code = 500
	rc.mu.Unlock()
	_, errOut, err := e.run("", "notify", "test")
	if err == nil || strings.Contains(err.Error()+errOut, "SECRETTOKEN123") || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("failure: %v", err)
	}

	out = e.mustRun("", "notify", "clear")
	if !strings.Contains(out, "alerts are off") {
		t.Errorf("clear: %s", out)
	}
	if out = e.mustRun("", "notify", "clear"); !strings.Contains(out, "no webhook was set") {
		t.Errorf("second clear: %s", out)
	}
}

func TestSetWebhookReadsFromStdin(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	rc := &hookRecv{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e.mustRun(srv.URL+"/x\n", "notify", "set-webhook")
	e.mustRun("", "notify", "test")
	if len(rc.got) != 1 {
		t.Errorf("hits = %d", len(rc.got))
	}
	v, _ := vault.Open("auto")
	if ok, _ := v.Has(notify.ChannelSecretKey("webhook", "url")); !ok {
		t.Error("not stored in the vault")
	}
}
