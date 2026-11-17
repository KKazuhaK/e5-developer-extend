package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/service"
)

func TestServicePrintShowsAllPlatformsAndTouchesNothing(t *testing.T) {
	e := newEnv(t)
	want := map[string][]string{
		"linux":   {"Type=notify", "WatchdogSec=120", "systemctl --user enable --now e5extend.service", "loginctl show-user"},
		"darwin":  {"io.github.kkazuhak.e5extend", "<key>ThrottleInterval</key>", "launchctl bootstrap gui/501"},
		"windows": {"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>", "e5extend-bg.exe", "schtasks /Create /TN e5extend /XML"},
	}
	for platform, markers := range want {
		out := e.mustRun("", "service", "install", "--print", "--platform", platform)
		for _, m := range markers {
			if !strings.Contains(out, m) {
				t.Errorf("%s: output lacks %q:\n%s", platform, m, out)
			}
		}
	}
	if calls := e.runner.Commands(); len(calls) != 0 {
		t.Errorf("--print ran commands: %v", calls)
	}
	if ents, _ := os.ReadDir(e.home); len(ents) != 0 {
		t.Errorf("--print wrote files into the home directory: %v", ents)
	}
	out := e.mustRun("", "service", "install", "--print", "--platform", "linux", "--key-file", "--enable-linger")
	if !strings.Contains(out, "LoadCredential=e5extend-master-key:") || !strings.Contains(out, "enable-linger") || !strings.Contains(out, "secret content not shown") {
		t.Errorf("flags not reflected:\n%s", out)
	}
	if _, _, err := e.run("", "service", "install", "--platform", "linux"); err == nil {
		t.Error("--platform without --print accepted")
	}
	if _, _, err := e.run("", "service", "install", "--print", "--platform", "plan9"); err == nil {
		t.Error("unknown platform accepted")
	}
}

func hostCommandName() string {
	return map[string]string{"linux": "systemctl", "darwin": "launchctl", "windows": "schtasks"}[runtime.GOOS]
}

func TestServiceInstallStatusUninstallUseFakeRunner(t *testing.T) {
	if hostCommandName() == "" {
		t.Skip("no service manager for this platform")
	}
	e := newEnv(t)
	e.runner.Replies = map[string]service.Output{"loginctl": {Stdout: "yes\n"}}
	out := e.mustRun("", "service", "install")
	if !strings.Contains(out, "installed") {
		t.Errorf("install output:\n%s", out)
	}
	found := false
	for _, c := range e.runner.Commands() {
		if strings.HasPrefix(c, hostCommandName()) {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s command recorded: %v", hostCommandName(), e.runner.Commands())
	}

	e.runner.Replies = map[string]service.Output{
		"systemctl --user is-enabled": {Stdout: "enabled"}, "systemctl --user is-active": {Stdout: "active"},
		"loginctl":                 {Stdout: "yes"},
		"launchctl print gui/501/": {Stdout: "state = running"},
		"schtasks /Query":          {Stdout: "<Task><Settings><Enabled>true</Enabled></Settings></Task>"},
	}
	out = e.mustRun("", "service", "status")
	if !strings.Contains(out, "installed: yes") || !strings.Contains(out, "enabled:   yes") || !strings.Contains(out, "engine:") {
		t.Errorf("status:\n%s", out)
	}
	out = e.mustRun("", "service", "uninstall")
	if !strings.Contains(out, "not touched") {
		t.Errorf("uninstall:\n%s", out)
	}
}

func TestServiceInstallRefusesTemporaryBinary(t *testing.T) {
	e := newEnv(t)
	e.exe = filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "e5extend")
	_, _, err := e.run("", "service", "install")
	if err == nil || !strings.Contains(err.Error(), "go run") {
		t.Errorf("err = %v", err)
	}
	if len(e.runner.Commands()) != 0 {
		t.Error("commands ran")
	}
}

func TestServiceKeyFileNeedsUsableBackend(t *testing.T) {
	e := newEnv(t)
	e.mustRun("", "config", "init")
	path := filepath.Join(e.cfgDir, "config.toml")
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), `backend = "auto"`, `backend = "keyring"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.run("", "service", "install", "--key-file"); err == nil || !strings.Contains(err.Error(), "keyring") {
		t.Errorf("err = %v", err)
	}
}

func TestKeyHint(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if h := keyHint("linux", false, env(nil)); !strings.Contains(h, "--key-file") || !strings.Contains(h, "file permissions") {
		t.Errorf("server hint: %q", h)
	}
	if h := keyHint("linux", false, env(map[string]string{"DISPLAY": ":0"})); h != "" {
		t.Errorf("desktop hint: %q", h)
	}
	if h := keyHint("linux", true, env(nil)); h != "" {
		t.Errorf("with --key-file: %q", h)
	}
	if h := keyHint("darwin", false, env(nil)); h != "" {
		t.Errorf("darwin: %q", h)
	}
	if h := keyHint("linux", false, env(map[string]string{"E5EXTEND_MASTER_KEY": "x", "DISPLAY": ":0"})); !strings.Contains(h, "does not inherit") {
		t.Errorf("env key: %q", h)
	}
	if h := keyHint("linux", false, env(map[string]string{"CREDENTIALS_DIRECTORY": "/c"})); h != "" {
		t.Errorf("credential: %q", h)
	}
}

func TestExitErrorIsNotPrinted(t *testing.T) {
	var ee exitError
	if !errors.As(error(exitError{code: 2}), &ee) || ee.code != 2 {
		t.Error("exitError not matched")
	}
}
