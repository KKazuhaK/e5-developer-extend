package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

func TestInitRefusesWithoutTerminal(t *testing.T) {
	e := newEnv(t)
	_, _, err := e.run("", "init")
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") || !strings.Contains(err.Error(), "account add") {
		t.Errorf("err = %v", err)
	}
	if _, statErr := os.Stat(e.cfgDir + "/config.toml"); statErr == nil {
		t.Error("init created files without a terminal")
	}
}

func TestInitScriptedHappyPath(t *testing.T) {
	e := newEnv(t)
	e.interactive = true
	e.runner.Replies = nil
	t.Setenv("DISPLAY", ":0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"value":[]}`)) }))
	defer srv.Close()
	e.graphBase = srv.URL
	hook := &hookRecv{}
	hs := httptest.NewServer(hook)
	defer hs.Close()
	useAuth(e, &fakeAuth{tok: goodToken()})

	answers := strings.Join([]string{
		"1",         // master key: system keyring
		"y",         // add an account
		"2",         // I already have an app registration
		"main",      // name
		"1",         // device code
		"t.example", // tenant
		testGUID,    // client id
		"y",         // sign in
		"y",         // first round
		"n",         // webhook (skipped here; set up below in the second run)
		"n",         // background job
	}, "\n") + "\n"
	out, errOut, err := e.run(answers, "init")
	if err != nil {
		t.Fatalf("%v\nout:\n%s\nerr:\n%s", err, out, errOut)
	}
	for _, want := range []string{"[1/9]", "created", "added account", "token ok; ", "APIs usable", "round done", "[9/9]", "Setup finished"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(e.loadCfg().Accounts) != 1 {
		t.Error("account not added")
	}
	if len(e.runner.Commands()) != 0 && strings.Contains(strings.Join(e.runner.Commands(), "|"), "enable --now") {
		t.Error("the job was installed although the answer was no")
	}

	// A second run skips what is done and sets up the webhook and the job.
	e.runner.Replies = nil
	if runtime.GOOS == "windows" {
		// The fake runner succeeds by default, which would make the task look
		// installed already; on a clean machine the query fails.
		e.runner.FailOn = []string{"schtasks /Query"}
	}
	answers2 := strings.Join([]string{
		"n",                               // sign in again: the fake sign-in leaves no token cache, so it is offered
		"n",                               // first round
		"y", hs.URL + "/h/SECRETTOKEN123", // webhook
		"y", "n", "n", // job: yes; key file no; linger no
	}, "\n") + "\n"
	out, errOut, err = e.run(answers2, "init")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	for _, want := range []string{"exists; skipped", "found a master key", "already configured; skipped", "test alert delivered"} {
		if !strings.Contains(out, want) {
			t.Errorf("second run lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out+errOut, "SECRETTOKEN123") {
		t.Error("webhook address echoed")
	}
	if len(hook.got) != 1 || len(e.runner.Commands()) == 0 {
		t.Errorf("hook hits=%d commands=%v", len(hook.got), e.runner.Commands())
	}
	if !strings.Contains(out, "installed and started") && !strings.Contains(out, "background job installed") {
		t.Errorf("job not installed:\n%s", out)
	}
	_ = vault.EnvMasterKey
}

func TestInitServerPathPrintsKey(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("showing a generated key is offered on Linux only (it needs --key-file for the service)")
	}
	e := newEnv(t)
	e.interactive = true
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	// Make the keyring look unusable so a key must be generated.
	answers := strings.Join([]string{"2", "n", "n", "n", "n"}, "\n") + "\n"
	out, _, err := e.run(answers, "init")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Your master key") || os.Getenv(vault.EnvMasterKey) == "" {
		t.Errorf("no key generated:\n%s", out)
	}
	t.Cleanup(func() { os.Unsetenv(vault.EnvMasterKey) })
}

func TestInitStopsCleanlyWhenInputEnds(t *testing.T) {
	e := newEnv(t)
	e.interactive = true
	_, _, err := e.run("", "init")
	if err == nil || !strings.Contains(err.Error(), "input ended") {
		t.Errorf("err = %v", err)
	}
}

// When the master key comes from the environment, the background job cannot
// see it, so the wizard has to hand it a key file.
func TestInitForcesKeyFileWhenKeyIsFromEnvironment(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("--key-file is Linux only")
	}
	e := newEnv(t)
	e.interactive = true
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	addDelegated(e, "main")
	useAuth(e, &fakeAuth{tok: goodToken()})
	answers := strings.Join([]string{"n", "n", "n", "y", "n"}, "\n") + "\n" // sign in, round, webhook, job, linger
	out, _, err := e.run(answers, "init")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "does not inherit") {
		t.Errorf("no explanation:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "master.key")); err != nil {
		t.Errorf("key file not written: %v", err)
	}
}
