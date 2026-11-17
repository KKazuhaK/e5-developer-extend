package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-renew/internal/doctor"
	"github.com/KKazuhaK/e5-renew/internal/vault"
)

func TestDoctorWithoutConfigFailsWithExitCode2(t *testing.T) {
	e := newEnv(t)
	out, errOut, err := e.run("", "doctor", "--offline")
	var ee exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "no configuration file yet") || !strings.Contains(out, "e5extend config init") || !strings.Contains(out, "offline") {
		t.Errorf("out:\n%s", out)
	}
	if strings.Contains(errOut, "Error:") {
		t.Errorf("cobra printed the exit error: %q", errOut)
	}
	for _, c := range e.runner.Commands() {
		if !strings.Contains(c, "is-enabled") && !strings.Contains(c, "is-active") && !strings.Contains(c, "launchctl") && !strings.Contains(c, "schtasks") && !strings.Contains(c, "loginctl") {
			t.Errorf("unexpected command %q", c)
		}
	}
}

func TestDoctorJSONWithAccount(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	addDelegated(e, "main")
	useAuth(e, &fakeAuth{tok: goodToken()})
	out, _, err := e.run("", "doctor", "--offline", "--json")
	var ee exitError
	if !errors.As(err, &ee) || ee.code != 2 { // the engine is not running
		t.Fatalf("err = %v", err)
	}
	var checks []doctor.Check
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	byID := map[string]doctor.Check{}
	for _, c := range checks {
		byID[c.ID] = c
	}
	for id, lvl := range map[string]doctor.Level{
		"config": doctor.OK, "engine": doctor.Fail, "account.main.activity": doctor.Warn, "notify": doctor.Warn,
	} {
		if byID[id].Level != lvl {
			t.Errorf("%s = %+v, want %s", id, byID[id], lvl)
		}
	}
	for _, id := range []string{"network", "clock", "account.main.token"} {
		if _, ok := byID[id]; ok {
			t.Errorf("%s ran offline", id)
		}
	}
	if strings.Contains(out, "me@contoso.com") || strings.Contains(out, "TOKEN") {
		t.Errorf("output leaks token data:\n%s", out)
	}
}

func TestDoctorOnlineUsesGraphBase(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	addDelegated(e, "main")
	useAuth(e, &fakeAuth{tok: goodToken()})
	e.graphBase = "http://127.0.0.1:1" // refuses connections
	out, _, _ := e.run("", "doctor")
	if !strings.Contains(out, "cannot reach Microsoft Graph") || !strings.Contains(out, "sign-in") {
		t.Errorf("out:\n%s", out)
	}
	_ = os.Getenv
}
