package service

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/<name>.golden; line endings are ignored so
// a checkout with CRLF conversion still matches.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); w != got {
		t.Errorf("%s differs from the golden file (run: go test ./internal/service -update)\n--- got ---\n%s\n--- want ---\n%s", name, got, w)
	}
}

func TestSystemdUnitGolden(t *testing.T) {
	base := SystemdParams{Exe: "/home/u/.local/bin/e5extend", ConfigDir: "/home/u/.config/e5extend", DataDir: "/home/u/.local/share/e5extend"}
	golden(t, "systemd", SystemdUnit(base))

	withKey := base
	withKey.KeyFile = "/home/u/.config/e5extend/master.key"
	golden(t, "systemd_keyfile", SystemdUnit(withKey))

	old := withKey
	old.KeyFileEnv = true
	golden(t, "systemd_keyfile_env", SystemdUnit(old))

	odd := SystemdParams{Exe: "/opt/my apps/e5extend", ConfigDir: "/home/u/my config/100%", DataDir: "/data"}
	golden(t, "systemd_quoting", SystemdUnit(odd))
}

func TestLaunchdPlistGolden(t *testing.T) {
	p := LaunchdParams{Exe: "/usr/local/bin/e5extend", LogFile: "/Users/u/Library/Application Support/e5extend/engine.log", LogPath: "/Users/u/Library/Logs/e5extend/engine.log"}
	golden(t, "launchd", LaunchdPlist(p))
	p.Env = map[string]string{"E5EXTEND_DATA_DIR": "/Volumes/x/data & more", "E5EXTEND_CONFIG_DIR": "/Volumes/x/cfg"}
	golden(t, "launchd_env", LaunchdPlist(p))
}

func TestTaskXMLGolden(t *testing.T) {
	golden(t, "task_bg", TaskXML(TaskParams{
		Exe: `C:\Users\u\bin\e5extend-bg.exe`, UserID: `PC\u`, LogFile: `C:\Users\u\AppData\Local\e5extend\engine.log`}))
	golden(t, "task_console", TaskXML(TaskParams{
		Exe: `C:\Program Files\e5extend\e5extend.exe`, UserID: `PC\u`, LogFile: `C:\Users\u\My Data\engine.log`}))
}

func TestTaskXMLKeyDefaults(t *testing.T) {
	x := TaskXML(TaskParams{Exe: `C:\e.exe`, UserID: `PC\u`, LogFile: `C:\l.log`})
	for _, want := range []string{
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<StartWhenAvailable>true</StartWhenAvailable>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<Interval>PT1M</Interval>", "<Count>999</Count>",
		"<LogonTrigger>",
	} {
		if !strings.Contains(x, want) {
			t.Errorf("task XML lacks %s", want)
		}
	}
}

func init() { retryWait, settleWait = 0, 0 }

func TestTaskXMLHasPeriodicTrigger(t *testing.T) {
	x := TaskXML(TaskParams{Exe: `C:\e.exe`, UserID: `PC\u`, LogFile: `C:\l.log`})
	for _, want := range []string{"<TimeTrigger>", "<Repetition>", "<Interval>PT5M</Interval>", "<StartBoundary>"} {
		if !strings.Contains(x, want) {
			t.Errorf("lacks %s", want)
		}
	}
	if strings.Contains(x, "<Duration>") {
		t.Error("repetition must have no duration limit")
	}
}

func TestUnitHasNoNetworkOnlineOrdering(t *testing.T) {
	u := SystemdUnit(SystemdParams{Exe: "/e", ConfigDir: "/c", DataDir: "/d"})
	if strings.Contains(u, "After=") || strings.Contains(u, "Wants=") {
		t.Errorf("unit orders against targets the user manager lacks:\n%s", u)
	}
}

func TestUTF16LE(t *testing.T) {
	b := utf16LE("a\u00e9\U0001F600")
	if b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("no byte order mark: % x", b[:2])
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 2; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	if got := string(utf16.Decode(u)); got != "a\u00e9\U0001F600" {
		t.Errorf("round trip = %q", got)
	}
}

func TestCheckExecutable(t *testing.T) {
	for _, bad := range []string{
		"/tmp/go-build123/b001/exe/e5extend",
		"/home/u/.cache/go-build/ab/exe/e5extend",
		"/tmp/e5extend",
		"/var/folders/xx/T/e5extend",
		`C:\Users\u\AppData\Local\Temp\go-build1\e5extend.exe`,
		`C:\Users\u\AppData\Local\Temp\e5extend.exe`,
		"/mnt/scratch/e5extend",
		"/private/var/folders/ab/xyz/T/AppTranslocation/1234/d/E5 Renew.app/Contents/MacOS/e5extend",
		"/Volumes/E5 Renew/E5 Renew.app/Contents/MacOS/e5extend",
	} {
		if err := CheckExecutable(bad, "/mnt/scratch"); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	for _, good := range []string{"/usr/local/bin/e5extend", "/home/u/.local/bin/e5extend", "/home/u/tmpfiles/e5extend", `C:\Program Files\e5extend\e5extend.exe`,
		"/Applications/E5 Renew.app/Contents/MacOS/e5extend", "/Volumes/Backup/bin/e5extend"} {
		if err := CheckExecutable(good, "/mnt/scratch"); err != nil {
			t.Errorf("%s refused: %v", good, err)
		}
	}
}

func TestPickWindowsExe(t *testing.T) {
	exists := func(set ...string) func(string) bool {
		return func(p string) bool {
			for _, s := range set {
				if s == p {
					return true
				}
			}
			return false
		}
	}
	got, warn := PickWindowsExe(`C:\app\e5extend.exe`, exists(`C:\app\e5extend-bg.exe`))
	if got != `C:\app\e5extend-bg.exe` || warn != "" {
		t.Errorf("with bg: %q %q", got, warn)
	}
	got, warn = PickWindowsExe(`C:\app\e5extend.exe`, exists())
	if got != `C:\app\e5extend.exe` || !strings.Contains(warn, "console window") {
		t.Errorf("without bg: %q %q", got, warn)
	}
	got, warn = PickWindowsExe(`C:\app\E5EXTEND-BG.EXE`, exists())
	if got != `C:\app\E5EXTEND-BG.EXE` || warn != "" {
		t.Errorf("already bg: %q %q", got, warn)
	}
}

func TestJoin(t *testing.T) {
	if got := join(Windows, `C:\Users\u\`, "AppData", "x.xml"); got != `C:\Users\u\AppData\x.xml` {
		t.Errorf("windows join = %q", got)
	}
	if got := join(Linux, "/home/u", ".config", "x"); got != "/home/u/.config/x" {
		t.Errorf("linux join = %q", got)
	}
}

func TestUnitQuoting(t *testing.T) {
	if got := unitWord("/a b/c"); got != `"/a b/c"` {
		t.Errorf("space: %s", got)
	}
	if got := unitWord("/100%/$x"); got != "/100%%/$$x" {
		t.Errorf("specials: %s", got)
	}
	if got := unitWord(`/a"b`); got != `"/a\"b"` {
		t.Errorf("quote: %s", got)
	}
}

func TestPlanFormatHidesSecrets(t *testing.T) {
	p := Plan{Files: []File{{Path: "/k", Mode: 0o600, Secret: true, Data: []byte("TOPSECRET")}, {Path: "/u", Mode: 0o644, Data: []byte("hello\n")}},
		Commands: []Command{{Args: []string{"x", "y"}, Optional: true, IfInstalled: true}}}
	out := p.Format()
	if strings.Contains(out, "TOPSECRET") || !strings.Contains(out, "hello") || !strings.Contains(out, "only if already installed") {
		t.Errorf("format:\n%s", out)
	}
}

func TestFakeRunnerRepliesAndFailures(t *testing.T) {
	f := &FakeRunner{Replies: map[string]Output{"a b": {Stdout: "x"}, "a": {Stdout: "short"}}, FailOn: []string{"bad"}}
	if o, err := f.Run(nil, "a", "b", "c"); err != nil || o.Stdout != "x" { //nolint:staticcheck
		t.Errorf("longest prefix: %v %v", o, err)
	}
	if _, err := f.Run(nil, "bad"); err == nil { //nolint:staticcheck
		t.Error("FailOn ignored")
	}
	if got := f.Commands(); len(got) != 2 || got[0] != "a b c" {
		t.Errorf("calls = %v", got)
	}
}

func TestTaskPassesExplicitDirs(t *testing.T) {
	x := TaskXML(TaskParams{Exe: `C:\e.exe`, UserID: `PC\u`, LogFile: `C:\d d\engine.log`, ConfigDir: `C:\c c`, DataDir: `C:\d d`})
	want := `--config-dir &quot;C:\c c&quot; --data-dir &quot;C:\d d&quot; run --log-format json --log-file &quot;C:\d d\engine.log&quot;`
	if !strings.Contains(x, "<Arguments>"+want+"</Arguments>") {
		t.Errorf("arguments not as expected:\n%s", x)
	}
	if plain := TaskXML(TaskParams{Exe: `C:\e.exe`, UserID: `PC\u`, LogFile: `C:\l.log`}); strings.Contains(plain, "--config-dir") {
		t.Error("dirs passed although none were set")
	}
}
