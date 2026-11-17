package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/update"
	"github.com/KKazuhaK/e5-developer-extend/internal/update/updatetest"
)

// updateRig is the services rig plus a fake GitHub and a machine that runs an
// older program.
type updateRig struct {
	*rig
	srv *updatetest.Server
	exe string
	svc *UpdateService
}

func newUpdateRig(t *testing.T, files func(ver string) map[string][]byte) *updateRig {
	t.Helper()
	r := newRig(t)
	u := &updateRig{rig: r, svc: NewUpdateService(r.b)}
	bin := t.TempDir()
	u.exe = filepath.Join(bin, "e5extend")
	if err := os.WriteFile(u.exe, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if files == nil {
		files = func(ver string) map[string][]byte {
			return map[string][]byte{"e5extend_" + ver + "_linux_amd64.tar.gz": updatetest.TarGz(t, "e5extend", "NEW PROGRAM")}
		}
	}
	u.srv = updatetest.New(t, "v0.3.0", files("0.3.0"))
	u.hooks("0.2.0", "")
	return u
}

// hooks describes a standalone Linux binary of the given version.
func (u *updateRig) hooks(version, docker string) {
	u.b.Ops.Update = ops.UpdateHooks{
		APIBase: u.srv.URL, PublicKey: u.srv.PublicKey(), AllowDownload: func(string) bool { return true },
		Version: version, GOOS: "linux", GOARCH: "amd64", TempRoot: u.t.TempDir(), Geteuid: func() int { return 1000 },
		Env: func() update.Env {
			return update.Env{
				GOOS: "linux", GOARCH: "amd64", Exe: u.exe,
				Getenv: func(k string) string {
					if k == "E5EXTEND_DOCKER" {
						return docker
					}
					return ""
				},
				Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
				Run:    func(context.Context, string, ...string) (string, error) { return "", os.ErrNotExist },
			}
		},
	}
}

// progress returns the UpdateProgress events so far.
func (u *updateRig) progress() []UpdateProgress {
	u.ev.mu.Lock()
	defer u.ev.mu.Unlock()
	var out []UpdateProgress
	for _, e := range u.ev.list {
		if p, ok := e.Data.(UpdateProgress); ok && e.Name == UpdateProgressEvent {
			out = append(out, p)
		}
	}
	return out
}

// waitEnd waits for the "done" or "error" report.
func (u *updateRig) waitEnd() UpdateProgress {
	u.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range u.progress() {
			if p.Stage == "done" || p.Stage == "error" {
				return p
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	u.t.Fatalf("the upgrade did not end; reports: %+v", u.progress())
	return UpdateProgress{}
}

func (u *updateRig) program() string {
	b, _ := os.ReadFile(u.exe)
	return string(b)
}

func TestUpdateServiceInfoAndCheck(t *testing.T) {
	u := newUpdateRig(t, nil)
	info, err := u.svc.Info()
	if err != nil || info.Available || info.Current != "0.2.0" || info.Mode != "auto" || info.Kind != "linux-standalone" {
		t.Fatalf("before any check: %+v %v", info, err)
	}
	info, err = u.svc.Check(true)
	if err != nil || !info.Available || info.Latest != "0.3.0" || info.URL != "https://example.test/release/v0.3.0" {
		t.Fatalf("check: %+v %v", info, err)
	}
	// The next Info (another window, the tray) has it without a request.
	info, _ = u.svc.Info()
	if !info.Available || info.CheckedAt == nil {
		t.Fatalf("info after the check: %+v", info)
	}
	// A window's own check on opening only goes out when due: not again within the day.
	before := u.srv.Downloads()
	if _, err := u.svc.Check(false); err != nil {
		t.Fatal(err)
	}
	if u.srv.Downloads() != before {
		t.Fatal("downloaded while only checking")
	}
	// A development build is never offered an upgrade.
	u.hooks("dev", "")
	info, _ = u.svc.Check(true)
	if info.Available || !info.Dev || info.Mode != "manual" || info.Reason != "dev" {
		t.Fatalf("dev build: %+v", info)
	}
}

func TestUpdateServiceInstallReportsStagesAndReplacesTheProgram(t *testing.T) {
	u := newUpdateRig(t, nil)
	if err := u.svc.Install(); err != nil {
		t.Fatal(err)
	}
	end := u.waitEnd()
	if end.Stage != "done" || end.Result == nil || !end.Result.Done || end.Result.Version != "0.3.0" {
		t.Fatalf("end: %+v", end)
	}
	if u.program() != "NEW PROGRAM" {
		t.Fatalf("program = %q", u.program())
	}
	var order []string
	for _, p := range u.progress() {
		if len(order) == 0 || order[len(order)-1] != p.Stage {
			order = append(order, p.Stage)
		}
	}
	if got := strings.Join(order, ","); got != "download,verify,install,done" {
		t.Fatalf("stages = %s", got)
	}
	if !u.b.Idle() {
		t.Fatal("the backend still counts the upgrade as running")
	}
	// Up to date now: nothing to install, and it says so.
	u.hooks("0.3.0", "")
	if err := u.svc.Install(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range u.progress() {
			if p.Stage == "done" && p.Result != nil && p.Result.UpToDate {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no 'up to date' report: %+v", u.progress())
}

func TestUpdateServiceOnlyOneUpgradeAtATime(t *testing.T) {
	u := newUpdateRig(t, nil)
	gate := make(chan struct{})
	var entered atomic.Bool
	hooks := u.b.Ops.Update
	hooks.AllowDownload = func(string) bool { entered.Store(true); <-gate; return true }
	u.b.Ops.Update = hooks
	if err := u.svc.Install(); err != nil {
		t.Fatal(err)
	}
	for !entered.Load() {
		time.Sleep(time.Millisecond)
	}
	if err := u.svc.Install(); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a second upgrade: %v", err)
	}
	if u.b.Idle() {
		t.Fatal("a running upgrade must keep the window from exiting")
	}
	close(gate)
	if end := u.waitEnd(); end.Stage != "done" {
		t.Fatalf("end: %+v", end)
	}
}

func TestUpdateServiceErrorCodes(t *testing.T) {
	other, _ := update.GenerateKey()
	cases := []struct {
		name   string
		tweak  func(u *updateRig)
		code   string
		stays  string
		wantDL bool
	}{
		{"no signing key in this build", func(u *updateRig) {
			h := u.b.Ops.Update
			h.PublicKey = update.PublicKeyText // the placeholder
			u.b.Ops.Update = h
		}, "manual_no_key", "OLD", false},
		{"signed by another key", func(u *updateRig) {
			h := u.b.Ops.Update
			h.PublicKey = other.Public().String()
			u.b.Ops.Update = h
		}, "bad_signature", "OLD", true},
		{"docker", func(u *updateRig) { u.hooks("0.2.0", "1") }, "manual_docker", "OLD", false},
		{"development build", func(u *updateRig) { u.hooks("dev", "") }, "manual_dev", "OLD", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := newUpdateRig(t, nil)
			c.tweak(u)
			if err := u.svc.Install(); err != nil {
				t.Fatal(err)
			}
			end := u.waitEnd()
			if end.Stage != "error" || end.Code != c.code || end.Error == "" {
				t.Fatalf("end: %+v", end)
			}
			if u.program() != c.stays {
				t.Fatalf("a refused upgrade changed the program: %q", u.program())
			}
			if (u.srv.Downloads() > 0) != c.wantDL {
				t.Fatalf("downloads = %d", u.srv.Downloads())
			}
			if !u.b.Idle() {
				t.Fatal("still busy after the error")
			}
		})
	}
}

func TestUpdateServiceWindowsInstallerStartsAndExits(t *testing.T) {
	setup := "E5-Developer-Extend_0.3.0_windows_amd64_setup.exe"
	u := newUpdateRig(t, func(string) map[string][]byte { return map[string][]byte{setup: []byte("MZ installer")} })
	var started []string
	var exited atomic.Bool
	u.b.Exit = func() { exited.Store(true) }
	old := exitDelay
	exitDelay = time.Millisecond
	t.Cleanup(func() { exitDelay = old })

	h := u.b.Ops.Update
	h.GOOS = "windows"
	h.StartDetached = func(name string, args ...string) error {
		started = append([]string{filepath.Base(name)}, args...)
		return nil
	}
	h.Env = func() update.Env {
		dir := `C:\Users\me\AppData\Local\Programs\E5 Developer Extend`
		return update.Env{GOOS: "windows", GOARCH: "amd64", Exe: dir + `\e5extend-gui.exe`,
			Exists: func(p string) bool { return p == dir+`\uninstall.exe` }}
	}
	u.b.Ops.Update = h
	if err := u.svc.Install(); err != nil {
		t.Fatal(err)
	}
	end := u.waitEnd()
	if end.Stage != "done" || end.Result == nil || !end.Result.ExitNow {
		t.Fatalf("end: %+v", end)
	}
	if strings.Join(started, " ") != setup+" /S" {
		t.Fatalf("started %v", started)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !exited.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !exited.Load() {
		t.Fatal("the app did not exit to let the installer replace it")
	}
}

func TestUpdateServiceRestart(t *testing.T) {
	u := newUpdateRig(t, nil)
	if err := u.svc.Restart(); err == nil {
		t.Fatal("restart without a way to restart")
	}
	called := false
	u.b.RestartApp = func() error { called = true; return nil }
	if err := u.svc.Restart(); err != nil || !called {
		t.Fatalf("restart: %v called=%v", err, called)
	}
}
