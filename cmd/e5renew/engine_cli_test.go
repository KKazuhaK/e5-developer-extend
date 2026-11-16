package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/engine"
	"github.com/KKazuhaK/e5-renew/internal/store"
)

func addDelegated(e *env, name string) {
	e.t.Helper()
	e.mustRun("", "account", "add", name, "--auth", "device_code", "--tenant", "t.example", "--client-id", testGUID)
}

func useAuth(e *env, fa *fakeAuth) {
	e.newAuth = func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return fa, nil }
}

func goodToken() auth.Token {
	return auth.Token{AccessToken: "TOKEN", Account: "me@contoso.com", ExpiresOn: time.Now().Add(time.Hour),
		Scopes: []string{"Mail.Read", "User.Read", "Contacts.Read"}}
}

func TestCatalogList(t *testing.T) {
	e := newEnv(t)
	out := e.mustRun("", "catalog", "list")
	for _, want := range []string{"mail.messages", "reports.active", "drive.info", "delegated"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if _, _, err := e.run("", "catalog", "list", "--account", "ghost"); err == nil {
		t.Error("unknown account accepted")
	}

	addDelegated(e, "main")
	out = e.mustRun("", "catalog", "list", "--account", "main")
	if !strings.Contains(out, "no granted permissions recorded") {
		t.Errorf("no hint about missing grants:\n%s", out)
	}
	st := store.New(e.dataDir)
	state := store.NewState()
	state.Accounts["main"] = &store.AccountState{Status: store.StatusOK, GrantedScopes: []string{"Mail.Read"}}
	if err := st.SaveState(state); err != nil {
		t.Fatal(err)
	}
	out = e.mustRun("", "catalog", "list", "--account", "main")
	row := func(id string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, id+" ") {
				return l
			}
		}
		return ""
	}
	if !strings.Contains(row("mail.messages"), "yes") || !strings.Contains(row("cal.list"), "missing permission") {
		t.Errorf("availability wrong:\n%s", out)
	}
}

func TestPauseAndResume(t *testing.T) {
	e := newEnv(t)
	addDelegated(e, "main")
	e.mustRun("", "account", "pause", "main")
	if !e.loadCfg().Accounts[0].Paused {
		t.Fatal("not paused in config")
	}
	e.mustRun("", "account", "resume", "main")
	if e.loadCfg().Accounts[0].Paused {
		t.Fatal("still paused")
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, "trigger", "main.resume")); err != nil {
		t.Errorf("no resume trigger: %v", err)
	}
	if _, _, err := e.run("", "account", "pause", "ghost"); err == nil {
		t.Error("unknown account accepted")
	}

	// With an engine running, a reload is requested too.
	release, err := engine.AcquireLock(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	e.mustRun("", "account", "pause", "main")
	if _, err := os.Stat(filepath.Join(e.dataDir, "trigger", ".reload")); err != nil {
		t.Errorf("no reload trigger: %v", err)
	}
}

func TestRunNowWithoutEngineReportsLoginProblem(t *testing.T) {
	e := newEnv(t)
	addDelegated(e, "main")
	useAuth(e, &fakeAuth{err: fmt.Errorf("refresh: %w", auth.ErrLoginRequired)})
	out, _, err := e.run("", "run-now")
	if err == nil || !strings.Contains(err.Error(), "e5renew account login main") {
		t.Fatalf("err = %v\nout = %s", err, out)
	}
	if !strings.Contains(out, "no engine is running") {
		t.Errorf("out = %s", out)
	}
	// The failure is also in the state, so `status` shows it.
	out = e.mustRun("", "status")
	if !strings.Contains(out, "needs_attention") || !strings.Contains(out, "engine: not running") {
		t.Errorf("status:\n%s", out)
	}
}

func TestRunNowNetworkProblem(t *testing.T) {
	e := newEnv(t)
	addDelegated(e, "main")
	useAuth(e, &fakeAuth{err: fmt.Errorf("dial tcp: %w", auth.ErrTransient)})
	_, _, err := e.run("", "run-now", "main")
	if err == nil || !strings.Contains(err.Error(), "could not reach") || !strings.Contains(err.Error(), "proxy") {
		t.Errorf("err = %v", err)
	}
}

func TestRunNowSuccessAndLogs(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer TOKEN" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer srv.Close()
	e.graphBase = srv.URL
	addDelegated(e, "main")
	useAuth(e, &fakeAuth{tok: goodToken()})

	out, _, err := e.run("", "run-now", "main")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "round done") || !strings.Contains(out, "200") {
		t.Errorf("out:\n%s", out)
	}

	out = e.mustRun("", "logs", "main", "-n", "3")
	if got := len(strings.Split(strings.TrimSpace(out), "\n")); got != 3 {
		t.Errorf("-n 3 printed %d lines:\n%s", got, out)
	}
	if !strings.Contains(out, "round_end") {
		t.Errorf("logs:\n%s", out)
	}
	if out := e.mustRun("", "logs", "other"); !strings.Contains(out, "no log entries") {
		t.Errorf("other account:\n%s", out)
	}
	if out := e.mustRun("", "logs", "--since", "1ns"); !strings.Contains(out, "no log entries") {
		t.Errorf("--since:\n%s", out)
	}

	out = e.mustRun("", "status")
	if !strings.Contains(out, "main") || !strings.Contains(out, " ok ") {
		t.Errorf("status:\n%s", out)
	}
	if strings.Contains(out, "0/0") {
		t.Errorf("today's counts missing:\n%s", out)
	}
}

func TestRunNowWithEngineWritesTrigger(t *testing.T) {
	e := newEnv(t)
	addDelegated(e, "main")
	release, err := engine.AcquireLock(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	out := e.mustRun("", "run-now")
	if !strings.Contains(out, "the engine is running") {
		t.Errorf("out: %s", out)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, "trigger", "main")); err != nil {
		t.Errorf("no trigger file: %v", err)
	}
	if out := e.mustRun("", "status"); !strings.Contains(out, "engine: running") {
		t.Errorf("status:\n%s", out)
	}
	if _, _, err := e.run("", "run-now", "ghost"); err == nil {
		t.Error("unknown account accepted")
	}
}

func TestStatusMarksStaleAccounts(t *testing.T) {
	e := newEnv(t)
	addDelegated(e, "main")
	state := store.NewState()
	state.Accounts["main"] = &store.AccountState{Status: store.StatusOK, LastSuccess: time.Now().Add(-4 * 24 * time.Hour),
		Reason: "something to read", Totals: store.Totals{Rounds: 5}}
	if err := store.New(e.dataDir).SaveState(state); err != nil {
		t.Fatal(err)
	}
	out := e.mustRun("", "status")
	if !strings.Contains(out, "(stale)") || !strings.Contains(out, "something to read") {
		t.Errorf("status:\n%s", out)
	}
}

func TestRunCommandStartsAndStops(t *testing.T) {
	e := newEnv(t)
	addDelegated(e, "main")
	useAuth(e, &fakeAuth{err: auth.ErrTransient})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := e.runCtx(ctx, "", "run", "--log-format", "json")
		done <- err
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if hb, err := engine.ReadHeartbeat(e.dataDir); err == nil && !hb.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("engine did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A second engine is refused.
	if _, _, err := e.run("", "run"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second run: %v", err)
	}
	if out := e.mustRun("", "status"); !strings.Contains(out, "engine: running") {
		t.Errorf("status:\n%s", out)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, "state.json")); err != nil {
		t.Errorf("state.json missing after shutdown: %v", err)
	}
	if engine.Running(e.dataDir) {
		t.Error("lock held after shutdown")
	}
}

func TestRunRejectsBadLogFormat(t *testing.T) {
	e := newEnv(t)
	addDelegated(e, "main")
	if _, _, err := e.run("", "run", "--log-format", "xml"); err == nil {
		t.Error("bad format accepted")
	}
}
