package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
)

func TestSaveDialogOptions(t *testing.T) {
	o := saveDialogOptions("Save the backup file", "e5extend-backup-20261116.e5backup")
	if o.Title != "Save the backup file" || o.Message != o.Title {
		t.Errorf("title %q, message %q", o.Title, o.Message)
	}
	if o.Filename != "e5extend-backup-20261116.e5backup" {
		t.Errorf("filename %q", o.Filename)
	}
	if len(o.Filters) != 1 || o.Filters[0].Pattern != "*.e5backup" {
		t.Errorf("filters %+v", o.Filters)
	}
}

func TestRestartScriptWaitsForTheOldProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the helper is a shell script")
	}
	sleeper := exec.Command("sleep", "2")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = sleeper.Wait(); close(done) }()
	defer func() { _ = sleeper.Process.Kill(); <-done }()
	old := procRef{sleeper.Process.Pid, ops.ProcStart(sleeper.Process.Pid)}

	out := filepath.Join(t.TempDir(), "started")
	script := restartScript([]procRef{{}, old}, 30, false)
	start := time.Now()
	// $0 is "touch", "$@" its argument: the "start" of the app.
	if err := exec.Command("sh", "-c", script, "touch", out).Run(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 1500*time.Millisecond {
		t.Errorf("started after %v, before the old process exited", d)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("the app was not started: %v", err)
	}
	if s := restartScript([]procRef{{}}, 30, false); strings.Contains(s, "kill") {
		t.Errorf("waits for nothing: %s", s)
	}
}

func TestRestartScriptGivesUpOnlyOnTheVerifiedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the helper is a shell script")
	}
	self := os.Getpid()
	run := func(p procRef, wait int) (time.Duration, bool, error) {
		out := filepath.Join(t.TempDir(), "started")
		start := time.Now()
		err := exec.Command("sh", "-c", restartScript([]procRef{p}, wait, false), "touch", out).Run()
		_, serr := os.Stat(out)
		return time.Since(start), serr == nil, err
	}
	// A running process that is verifiably the tray is waited for, then left alone.
	if st := ops.ProcStart(self); st != "" {
		d, started, err := run(procRef{self, st}, 2)
		if err == nil || started || d < 1500*time.Millisecond {
			t.Errorf("verified process: err=%v started=%v after %v", err, started, d)
		}
		// The same id with another start time is a reused id: start at once.
		d, started, err = run(procRef{self, "1"}, 30)
		if err != nil || !started || d > 5*time.Second {
			t.Errorf("reused pid: err=%v started=%v after %v", err, started, d)
		}
	}
	// Without a start time the process cannot be verified: wait the limit, then start anyway.
	d, started, err := run(procRef{self, ""}, 2)
	if err != nil || !started || d < 1500*time.Millisecond || d > 6*time.Second {
		t.Errorf("unverifiable process: err=%v started=%v after %v", err, started, d)
	}
}

func TestReadTray(t *testing.T) {
	f := filepath.Join(t.TempDir(), "gui-tray.pid")
	alive := func(p int, start string) bool { return p == 42 && (start == "" || start == "777") }
	if got := readTray(f, alive); got.pid != 0 {
		t.Errorf("no file: %v", got)
	}
	cases := map[string]procRef{
		"42\n":     {42, ""},
		"42 777\n": {42, "777"},
		"42 888\n": {},
		"43\n":     {},
		"junk":     {},
		"-1":       {},
	}
	for content, want := range cases {
		if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readTray(f, alive); got != want {
			t.Errorf("%q: got %v, want %v", content, got, want)
		}
	}
}
