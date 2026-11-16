package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS == "windows" {
		// The fake runner succeeds by default, which would make the scheduled
		// task look installed; on a clean machine the query fails.
		e.runner.FailOn = []string{"schtasks /Query"}
	}
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
	if !strings.Contains(out, "e5renew service install") || !strings.Contains(out, "e5renew run") {
		t.Errorf("status must say how to start the engine:\n%s", out)
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

func TestRunWritesLogFileAndNotifiesSystemd(t *testing.T) {
	e := newEnv(t)
	addDelegated(e, "main")
	useAuth(e, &fakeAuth{err: auth.ErrTransient})
	logPath := filepath.Join(e.dataDir, "logs-out", "engine.log")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, errOut, err := e.runCtx(ctx, "", "run", "--log-format", "json", "--log-file", logPath)
		if errOut != "" {
			t.Errorf("run wrote to stderr although --log-file was given: %q", errOut)
		}
		done <- err
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if b, err := os.ReadFile(logPath); err == nil && strings.Contains(string(b), `"event"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no log file content")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRotatingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d", "engine.log")
	r, err := openRotating(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	line := strings.Repeat("x", 40) + "\n"
	for i := 0; i < 10; i++ {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	cur, _ := os.ReadFile(p)
	old, err := os.ReadFile(p + ".1")
	if err != nil || len(cur) > 100 || len(old) == 0 {
		t.Errorf("rotation: cur=%d old=%d err=%v", len(cur), len(old), err)
	}
}

// Under the Windows background build there is no console, so startup errors
// must end up in the log file.
func TestRunStartupErrorsGoToTheLogFile(t *testing.T) {
	e := newEnv(t) // no config file
	logPath := filepath.Join(e.dataDir, "engine.log")
	_, errOut, err := e.run("", "run", "--log-file", logPath)
	if err == nil {
		t.Fatal("run without a config succeeded")
	}
	b, rerr := os.ReadFile(logPath)
	if rerr != nil || !strings.Contains(string(b), "e5renew run failed") || !strings.Contains(string(b), "config file not found") {
		t.Errorf("log file: %v\n%s", rerr, b)
	}
	if strings.Contains(errOut, "run failed") {
		t.Errorf("the log line also went to stderr: %q", errOut)
	}

	// A bad --log-format is logged too (as text).
	e.mustRun("", "config", "init")
	if _, _, err := e.run("", "run", "--log-format", "xml", "--log-file", logPath); err == nil {
		t.Fatal("bad format accepted")
	}
	b, _ = os.ReadFile(logPath)
	if !strings.Contains(string(b), "--log-format must be text or json") {
		t.Errorf("bad format not logged:\n%s", b)
	}

	// An engine that is already running.
	release, err := engine.AcquireLock(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, _, err := e.run("", "run", "--log-file", logPath); err == nil {
		t.Fatal("second engine started")
	}
	b, _ = os.ReadFile(logPath)
	if !strings.Contains(string(b), "already running") {
		t.Errorf("lock error not logged:\n%s", b)
	}
}
