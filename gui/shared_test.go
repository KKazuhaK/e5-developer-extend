package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

	out := filepath.Join(t.TempDir(), "started")
	script := restartScript([]int{0, sleeper.Process.Pid}, 30, false)
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

	// With nothing to wait for it starts at once; a process that never exits
	// is waited for no longer than the limit.
	if s := restartScript([]int{0}, 30, false); strings.Contains(s, "kill") {
		t.Errorf("waits for nothing: %s", s)
	}
	start = time.Now()
	if err := exec.Command("sh", "-c", restartScript([]int{os.Getpid()}, 2, false), "true").Run(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 1500*time.Millisecond || d > 6*time.Second {
		t.Errorf("gave up after %v, want about 2 s", d)
	}
}

func TestReadTrayPID(t *testing.T) {
	f := filepath.Join(t.TempDir(), "gui-tray.pid")
	alive := func(p int) bool { return p == 42 }
	if got := readTrayPID(f, alive); got != 0 {
		t.Errorf("no file: %d", got)
	}
	for content, want := range map[string]int{"42\n": 42, "43\n": 0, "junk": 0, "-1": 0} {
		if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readTrayPID(f, alive); got != want {
			t.Errorf("%q: got %d, want %d", content, got, want)
		}
	}
}
