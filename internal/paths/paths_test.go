package paths

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		env    map[string]string
		home   string
		config bool
		want   string
	}{
		{"linux config default", "linux", nil, "/home/u", true, "/home/u/.config/e5extend"},
		{"linux data default", "linux", nil, "/home/u", false, "/home/u/.local/share/e5extend"},
		{"linux xdg config", "linux", map[string]string{"XDG_CONFIG_HOME": "/x/c"}, "/home/u", true, "/x/c/e5extend"},
		{"linux xdg data", "linux", map[string]string{"XDG_DATA_HOME": "/x/d"}, "/home/u", false, "/x/d/e5extend"},
		{"linux relative xdg ignored", "linux", map[string]string{"XDG_DATA_HOME": "rel"}, "/home/u", false, "/home/u/.local/share/e5extend"},
		{"darwin config", "darwin", nil, "/Users/u", true, "/Users/u/Library/Application Support/e5extend"},
		{"darwin data", "darwin", nil, "/Users/u", false, "/Users/u/Library/Application Support/e5extend"},
		{"windows config", "windows", map[string]string{"APPDATA": "/ad", "LOCALAPPDATA": "/lad"}, "", true, filepath.Join("/ad", "e5extend")},
		{"windows data", "windows", map[string]string{"APPDATA": "/ad", "LOCALAPPDATA": "/lad"}, "", false, filepath.Join("/lad", "e5extend")},
		{"override config", "linux", map[string]string{"E5EXTEND_CONFIG_DIR": "/o/c"}, "/home/u", true, "/o/c"},
		{"override data", "windows", map[string]string{"E5EXTEND_DATA_DIR": "/o/d"}, "", false, "/o/d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolve(tt.goos, env(tt.env), tt.home, tt.config)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.ToSlash(got) != filepath.ToSlash(tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	if _, err := resolve("linux", env(nil), "", true); err == nil {
		t.Error("expected error without home")
	}
	if _, err := resolve("windows", env(nil), "", true); err == nil {
		t.Error("expected error without APPDATA")
	}
}

func TestEnvOverrideAndFiles(t *testing.T) {
	cfg, data := t.TempDir(), t.TempDir()
	t.Setenv("E5EXTEND_CONFIG_DIR", cfg)
	t.Setenv("E5EXTEND_DATA_DIR", data)
	if got, _ := ConfigFile(); got != filepath.Join(cfg, "config.toml") {
		t.Errorf("ConfigFile = %q", got)
	}
	if got, _ := VaultDir(); got != filepath.Join(data, "vault") {
		t.Errorf("VaultDir = %q", got)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "f.txt")
	for _, content := range []string{"one", "two"} {
		if err := WriteFileAtomic(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != content {
			t.Fatalf("read = %q, %v", got, err)
		}
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(path))
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v", di.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("leftover temp files: %v", entries)
	}
}

func TestEnsureDirTightensExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits")
	}
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
}

func TestLockExcludes(t *testing.T) {
	old := LockTimeout
	LockTimeout = 200 * time.Millisecond
	defer func() { LockTimeout = old }()

	path := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := Lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(context.Background(), path); err == nil {
		t.Fatal("second lock must time out")
	}
	unlock()
	unlock2, err := Lock(context.Background(), path)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock2()
}

func TestRemoveStaleTemp(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	// Old leftovers of atomic writes go; a user's own *.tmp file and other
	// files stay, and so do fresh temp files that may still be in use.
	for _, n := range []string{"state.json.tmp-123", "heartbeat.tmp-hb", "notes.tmp", "keep.json"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
	}
	_ = os.WriteFile(filepath.Join(dir, "fresh.tmp-9"), nil, 0o600)
	RemoveStaleTemp(dir, 10*time.Minute)
	var left []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		left = append(left, e.Name())
	}
	want := []string{"fresh.tmp-9", "keep.json", "notes.tmp"}
	if strings.Join(left, ",") != strings.Join(want, ",") {
		t.Errorf("left %v, want %v", left, want)
	}
}
