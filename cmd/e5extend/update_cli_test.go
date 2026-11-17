package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/update"
	"github.com/KKazuhaK/e5-developer-extend/internal/update/updatetest"
)

// updateRig is a fake GitHub with one signed release, and a machine that runs
// an older standalone Linux binary.
type updateRig struct {
	e   *env
	srv *updatetest.Server
	exe string
}

func newUpdateRig(t *testing.T, tag string) *updateRig {
	t.Helper()
	r := &updateRig{e: newEnv(t)}
	bin := t.TempDir()
	r.exe = filepath.Join(bin, "e5extend")
	if err := os.WriteFile(r.exe, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	ver := strings.TrimPrefix(tag, "v")
	name := "e5extend_" + ver + "_linux_amd64.tar.gz"
	r.srv = updatetest.New(t, tag, map[string][]byte{name: updatetest.TarGz(t, "e5extend", "NEW PROGRAM")})
	return r
}

// run runs the command as a standalone Linux binary of the given version.
func (r *updateRig) run(t *testing.T, version, stdin string, args ...string) (string, string, error) {
	t.Helper()
	return r.runWith(t, version, "", stdin, args...)
}

// runWith is run with E5EXTEND_DOCKER set to dockerEnv.
func (r *updateRig) runWith(t *testing.T, version, dockerEnv, stdin string, args ...string) (string, string, error) {
	t.Helper()
	r.e.exe = r.exe
	r.e.update = ops.UpdateHooks{
		APIBase: r.srv.URL, PublicKey: r.srv.PublicKey(), AllowDownload: func(string) bool { return true },
		Version: version, GOOS: "linux", GOARCH: "amd64", TempRoot: t.TempDir(),
		Geteuid: func() int { return 1000 },
		Env: func() update.Env {
			return update.Env{
				GOOS: "linux", GOARCH: "amd64", Exe: r.exe,
				Getenv: func(k string) string {
					if k == "E5EXTEND_DOCKER" {
						return dockerEnv
					}
					return ""
				},
				Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
				// no package manager owns this file
				Run: func(context.Context, string, ...string) (string, error) { return "", os.ErrNotExist },
			}
		},
	}
	return r.e.run(stdin, args...)
}

func TestUpdateCheckReportsNewVersion(t *testing.T) {
	r := newUpdateRig(t, "v0.3.0")
	out, _, err := r.run(t, "0.2.0", "", "update", "check")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"this version: 0.2.0", "latest: 0.3.0", "a newer version is available", "e5extend update install", "linux-standalone"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// The answer is kept for the doctor and the tray, and no real GitHub was asked.
	if _, err := os.Stat(filepath.Join(r.e.dataDir, update.CacheFile)); err != nil {
		t.Fatalf("update.json: %v", err)
	}
	r.e.mustRun("", "config", "init")
	out, _, _ = r.run(t, "0.2.0", "", "doctor", "--offline")
	if !strings.Contains(out, "version 0.3.0 is available") {
		t.Errorf("the doctor does not mention the update:\n%s", out)
	}
}

func TestUpdateCheckJSONAndUpToDate(t *testing.T) {
	r := newUpdateRig(t, "v0.3.0")
	out, _, err := r.run(t, "0.2.0", "", "update", "check", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var info ops.UpdateInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !info.Available || info.Latest != "0.3.0" || info.Mode != "auto" || info.Kind != "linux-standalone" || info.Current != "0.2.0" {
		t.Fatalf("%+v", info)
	}
	out, _, _ = r.run(t, "0.3.0", "", "update", "check")
	if !strings.Contains(out, "you are up to date") || strings.Contains(out, "newer version") {
		t.Errorf("up to date:\n%s", out)
	}
	out, _, _ = r.run(t, "dev", "", "update", "check")
	if !strings.Contains(out, "development build") {
		t.Errorf("dev build:\n%s", out)
	}
}

func TestUpdateInstallReplacesTheProgram(t *testing.T) {
	r := newUpdateRig(t, "v0.3.0")
	// A user service that is installed gets restarted.
	unit := filepath.Join(r.e.home, ".config", "systemd", "user", "e5extend.service")
	_ = os.MkdirAll(filepath.Dir(unit), 0o755)
	_ = os.WriteFile(unit, []byte("[Service]\n"), 0o644)

	if _, _, err := r.run(t, "0.2.0", "", "update", "install"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("without a terminal and without --yes: %v", err)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != "OLD" {
		t.Fatal("changed the program without confirmation")
	}
	out, _, err := r.run(t, "0.2.0", "", "update", "install", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != "NEW PROGRAM" {
		t.Fatalf("program = %q\n%s", b, out)
	}
	if !strings.Contains(out, "upgraded to 0.3.0") {
		t.Errorf("output lacks the result:\n%s", out)
	}
	if runtime.GOOS == "linux" { // the fake service is a systemd unit
		if !strings.Contains(out, "restarted the background service") ||
			!strings.Contains(strings.Join(r.e.runner.Commands(), "\n"), "systemctl --user restart e5extend.service") {
			t.Errorf("service not restarted: %v\n%s", r.e.runner.Commands(), out)
		}
	}
	// Run again with the new version: nothing to do.
	out, _, err = r.run(t, "0.3.0", "", "update", "install", "--yes")
	if err != nil || !strings.Contains(out, "up to date") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestUpdateInstallInteractiveConfirmation(t *testing.T) {
	r := newUpdateRig(t, "v0.3.0")
	r.e.interactive = true
	if _, _, err := r.run(t, "0.2.0", "n\n", "update", "install"); err == nil || err.Error() != "aborted" {
		t.Fatalf("answering n: %v", err)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != "OLD" {
		t.Fatal("changed the program after answering no")
	}
	if _, _, err := r.run(t, "0.2.0", "y\n", "update", "install"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != "NEW PROGRAM" {
		t.Fatal("not upgraded after answering yes")
	}
}

func TestUpdateInstallRefusals(t *testing.T) {
	r := newUpdateRig(t, "v0.3.0")
	if _, _, err := r.run(t, "dev", "", "update", "install", "--yes"); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Errorf("dev build: %v", err)
	}
	out, _, err := r.runWith(t, "0.2.0", "1", "", "update", "install", "--yes")
	if err == nil || !strings.Contains(err.Error(), "Docker") || !strings.Contains(out, "version 0.3.0 is available") {
		t.Errorf("docker: err=%v out=%s", err, out)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != "OLD" {
		t.Fatal("a refused upgrade changed the program")
	}
	if n := r.srv.Downloads(); n != 0 {
		t.Errorf("%d downloads although the upgrade was refused", n)
	}
}

func TestUpdateCheckAutoOffSwitch(t *testing.T) {
	r := newUpdateRig(t, "v0.3.0")
	r.e.mustRun("", "config", "init")
	cfgPath := filepath.Join(r.e.cfgDir, "config.toml")
	b, _ := os.ReadFile(cfgPath)
	_ = os.WriteFile(cfgPath, []byte(strings.Replace(string(b), "check = true", "check = false", 1)), 0o600)
	// A manual check still works and says that the daily check is off.
	out, _, err := r.run(t, "0.2.0", "", "update", "check")
	if err != nil || !strings.Contains(out, "daily check is off") {
		t.Fatalf("%v\n%s", err, out)
	}
	// The doctor stays quiet while it is off.
	out, _, _ = r.run(t, "0.2.0", "", "doctor", "--offline")
	if strings.Contains(out, "is available") {
		t.Errorf("doctor mentions the update although the check is off:\n%s", out)
	}
}
