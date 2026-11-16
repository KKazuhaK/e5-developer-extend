package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func testOpts(t *testing.T, goos string) (InstallOptions, Env, *FakeRunner) {
	t.Helper()
	home := t.TempDir()
	fr := &FakeRunner{}
	env := Env{GOOS: goos, Home: home, UID: "501", User: "alice", Runner: fr, DataDir: filepath.Join(home, "data")}
	o := InstallOptions{Exe: filepath.Join(home, "bin", "e5renew"), ConfigDir: filepath.Join(home, "cfg"), DataDir: env.DataDir}
	return o, env, fr
}

func TestSystemdInstallCommandsAndFiles(t *testing.T) {
	o, env, fr := testOpts(t, Linux)
	fr.Replies = map[string]Output{"loginctl show-user alice -p Linger --value": {Stdout: "yes\n"}}
	m, _ := New(env)
	res, err := m.Install(o)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now e5renew.service",
		"loginctl show-user alice -p Linger --value",
	}
	if got := fr.Commands(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("commands = %v, want %v", got, want)
	}
	unit := join(Linux, env.Home, ".config", "systemd", "user", "e5renew.service")
	b, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(b), "Type=notify") {
		t.Fatalf("unit file: %v %s", err, b)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings %v", res.Warnings)
	}
}

func TestSystemdReinstallRestarts(t *testing.T) {
	o, env, fr := testOpts(t, Linux)
	fr.Replies = map[string]Output{"loginctl": {Stdout: "yes"}}
	m, _ := New(env)
	if _, err := m.Install(o); err != nil {
		t.Fatal(err)
	}
	for _, c := range fr.Commands() {
		if strings.Contains(c, "restart") {
			t.Fatalf("first install restarted: %v", fr.Commands())
		}
	}
	fr.Calls = nil
	if _, err := m.Install(o); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable --now e5renew.service", "systemctl --user restart e5renew.service", "loginctl show-user alice -p Linger --value"}
	if strings.Join(fr.Commands(), "|") != strings.Join(want, "|") {
		t.Errorf("reinstall = %v", fr.Commands())
	}
}

func TestSystemdKeyFileFallbackForOldSystemd(t *testing.T) {
	read := func(env Env) string {
		b, _ := os.ReadFile(join(Linux, env.Home, ".config", "systemd", "user", "e5renew.service"))
		return string(b)
	}
	for _, c := range []struct {
		version  string
		fallback bool
	}{{"systemd 259 (259.9-0ubuntu0.1)", false}, {"systemd 258 (258)", false}, {"systemd 255 (255.4)", true}, {"garbage", true}} {
		o, env, fr := testOpts(t, Linux)
		o.KeyFile = true
		o.MasterKey = func() ([]byte, error) { return make([]byte, 32), nil }
		fr.Replies = map[string]Output{"systemctl --version": {Stdout: c.version + "\n+PAM +AUDIT\n"}, "loginctl": {Stdout: "yes"}}
		m, _ := New(env)
		res, err := m.Install(o)
		if err != nil {
			t.Fatal(err)
		}
		u := read(env)
		if c.fallback != strings.Contains(u, "E5RENEW_MASTER_KEY_FILE=") || c.fallback == strings.Contains(u, "LoadCredential=") {
			t.Errorf("%q: unit does not match the expected form:\n%s", c.version, u)
		}
		// An unreadable version takes the fallback too, with its own warning.
		if c.fallback && (len(res.Warnings) == 0 || !(strings.Contains(res.Warnings[0], "older than") || strings.Contains(res.Warnings[0], "could not read"))) {
			t.Errorf("%q: no warning: %v", c.version, res.Warnings)
		}
	}
}

func TestSystemdKeyFile(t *testing.T) {
	o, env, fr := testOpts(t, Linux)
	o.KeyFile = true
	o.MasterKey = func() ([]byte, error) { return make([]byte, 32), nil }
	fr.Replies = map[string]Output{"systemctl --version": {Stdout: "systemd 259 (259.9)\n"}, "loginctl": {Stdout: "yes"}}
	m, _ := New(env)
	if _, err := m.Install(o); err != nil {
		t.Fatal(err)
	}
	key := join(Linux, o.ConfigDir, "master.key")
	fi, err := os.Stat(key)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeIsUnix() && fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(join(Linux, env.Home, ".config", "systemd", "user", "e5renew.service"))
	if !strings.Contains(string(b), "LoadCredential=e5renew-master-key:") {
		t.Errorf("unit lacks LoadCredential:\n%s", b)
	}

	o.MasterKey = nil
	if _, err := m.Plan(o); err == nil {
		t.Error("--key-file without a key source accepted")
	}
}

func TestSystemdLinger(t *testing.T) {
	// Off, without --enable-linger: a prominent warning.
	o, env, fr := testOpts(t, Linux)
	fr.Replies = map[string]Output{"loginctl show-user": {Stdout: "no\n"}}
	m, _ := New(env)
	res, err := m.Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "loginctl enable-linger alice") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	for _, c := range fr.Commands() {
		if strings.Contains(c, "enable-linger") {
			t.Errorf("ran %q without --enable-linger", c)
		}
	}

	// Off, with --enable-linger: it is turned on.
	o, env, fr = testOpts(t, Linux)
	fr.Replies = map[string]Output{"loginctl show-user": {Stdout: "no\n"}}
	o.EnableLinger = true
	m, _ = New(env)
	res, err = m.Install(o)
	if err != nil || len(res.Warnings) != 0 {
		t.Fatalf("%v %v", err, res.Warnings)
	}
	if got := fr.Commands(); got[len(got)-1] != "loginctl enable-linger alice" {
		t.Errorf("commands = %v", got)
	}

	// enable-linger fails: the sudo command is handed to the user.
	o, env, fr = testOpts(t, Linux)
	fr.Replies = map[string]Output{"loginctl show-user": {Stdout: "no\n"}}
	fr.FailOn = []string{"loginctl enable-linger"}
	o.EnableLinger = true
	m, _ = New(env)
	res, err = m.Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Manual) != 1 || res.Manual[0] != "sudo loginctl enable-linger alice" || len(res.Warnings) == 0 {
		t.Errorf("manual=%v warnings=%v", res.Manual, res.Warnings)
	}

	// loginctl unavailable: still a warning, not an error.
	o, env, fr = testOpts(t, Linux)
	fr.FailOn = []string{"loginctl"}
	m, _ = New(env)
	res, err = m.Install(o)
	if err != nil || len(res.Warnings) != 1 {
		t.Errorf("err=%v warnings=%v", err, res.Warnings)
	}
}

func TestSystemdInstallFailureHasHint(t *testing.T) {
	o, env, fr := testOpts(t, Linux)
	fr.FailOn = []string{"systemctl --user daemon-reload"}
	m, _ := New(env)
	_, err := m.Install(o)
	if err == nil || !strings.Contains(err.Error(), "XDG_RUNTIME_DIR") {
		t.Errorf("err = %v", err)
	}
}

func TestSystemdUninstallAndStatus(t *testing.T) {
	o, env, fr := testOpts(t, Linux)
	m, _ := New(env)

	// Not installed.
	st, err := m.Status()
	if err != nil || st.Installed || len(fr.Commands()) != 0 {
		t.Fatalf("status before install: %+v %v calls=%v", st, err, fr.Commands())
	}
	res, err := m.Uninstall()
	if err != nil || len(fr.Commands()) != 0 || len(res.Actions) != 1 {
		t.Fatalf("uninstall when absent: %v %v %v", res, err, fr.Commands())
	}

	o.KeyFile = true
	o.MasterKey = func() ([]byte, error) { return make([]byte, 32), nil }
	fr.Replies = map[string]Output{"loginctl": {Stdout: "yes"}}
	if _, err := m.Install(o); err != nil {
		t.Fatal(err)
	}
	fr.Calls = nil
	fr.Replies = map[string]Output{
		"systemctl --user is-enabled": {Stdout: "enabled\n"},
		"systemctl --user is-active":  {Stdout: "active\n"},
		"loginctl":                    {Stdout: "no\n"},
	}
	st, err = m.Status()
	if err != nil || !st.Installed || st.Enabled != Yes || st.Running != Yes || st.Linger != No {
		t.Fatalf("status = %+v %v", st, err)
	}
	// inactive makes systemctl exit non-zero but still print.
	fr.Replies["systemctl --user is-active"] = Output{Stdout: "inactive\n", ExitCode: 3}
	fr.Replies["systemctl --user is-enabled"] = Output{Stdout: "disabled\n", ExitCode: 1}
	if st, err = m.Status(); err != nil || st.Enabled != No || st.Running != No {
		t.Fatalf("status inactive = %+v %v", st, err)
	}
	// systemctl cannot talk to the user manager.
	fr.Replies["systemctl --user is-active"] = Output{ExitCode: 1, Stderr: "Failed to connect to bus"}
	fr.Replies["systemctl --user is-enabled"] = Output{ExitCode: 1, Stderr: "Failed to connect to bus"}
	if _, err = m.Status(); err == nil {
		t.Error("no answer from systemctl should be an error")
	}

	fr.Calls = nil
	res, err = m.Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl --user disable --now e5renew.service", "systemctl --user daemon-reload"}
	if strings.Join(fr.Commands(), "|") != strings.Join(want, "|") {
		t.Errorf("uninstall commands = %v", fr.Commands())
	}
	if _, err := os.Stat(join(Linux, env.Home, ".config", "systemd", "user", "e5renew.service")); !os.IsNotExist(err) {
		t.Errorf("unit file still there: %v", err)
	}
	if _, err := os.Stat(join(Linux, o.ConfigDir, "master.key")); err != nil {
		t.Errorf("master.key must stay: %v", err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[len(res.Warnings)-1], "master.key") {
		t.Errorf("no note about the key file: %v", res.Warnings)
	}
}

func TestLaunchdInstallUninstallStatus(t *testing.T) {
	o, env, fr := testOpts(t, Darwin)
	m, _ := New(env)
	if _, err := m.Install(InstallOptions{Exe: o.Exe, KeyFile: true}); err == nil {
		t.Error("--key-file accepted on macOS")
	}
	if _, err := m.Install(o); err != nil {
		t.Fatal(err)
	}
	plist := join(Darwin, env.Home, "Library", "LaunchAgents", "io.github.kkazuhak.e5renew.plist")
	if _, err := os.Stat(plist); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(join(Darwin, env.Home, "Library", "Logs", "e5renew")); err != nil {
		t.Errorf("log directory not created: %v", err)
	}
	// First install: no bootout; enable comes before bootstrap, because a
	// disabled label cannot be bootstrapped.
	want := []string{
		"launchctl enable gui/501/io.github.kkazuhak.e5renew",
		"launchctl bootstrap gui/501 " + plist,
	}
	if strings.Join(fr.Commands(), "|") != strings.Join(want, "|") {
		t.Errorf("first install = %v", fr.Commands())
	}
	// Reinstall: the old job is booted out first.
	fr.Calls = nil
	if _, err := m.Install(o); err != nil {
		t.Fatal(err)
	}
	if got := fr.Commands(); len(got) != 3 || got[0] != "launchctl bootout gui/501/io.github.kkazuhak.e5renew" ||
		got[1] != "launchctl enable gui/501/io.github.kkazuhak.e5renew" || !strings.HasPrefix(got[2], "launchctl bootstrap") {
		t.Errorf("reinstall = %v", got)
	}
	// The old job may still be exiting: bootstrap is tried once more.
	fr.Calls = nil
	fr.FailOnce = []string{"launchctl bootstrap"}
	if _, err := m.Install(o); err != nil {
		t.Fatalf("bootstrap retry: %v", err)
	}
	n := 0
	for _, c := range fr.Commands() {
		if strings.HasPrefix(c, "launchctl bootstrap") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("bootstrap ran %d times, want 2: %v", n, fr.Commands())
	}
	fr.Calls = nil
	fr.FailOn = []string{"launchctl bootstrap"}
	if _, err := m.Install(o); err == nil {
		t.Error("a bootstrap that keeps failing must be an error")
	}
	fr.FailOn = nil
	if b, _ := os.ReadFile(plist); !strings.Contains(string(b), "<string>--log-file</string>") {
		t.Errorf("plist lacks --log-file:\n%s", b)
	}

	fr.Calls = nil
	fr.Replies = map[string]Output{
		"launchctl print gui/501/": {Stdout: "state = running\n"},
		"launchctl print-disabled": {Stdout: `"io.github.kkazuhak.e5renew" => enabled`},
	}
	st, err := m.Status()
	if err != nil || !st.Installed || st.Enabled != Yes || st.Running != Yes {
		t.Fatalf("status = %+v %v", st, err)
	}
	fr.Replies["launchctl print-disabled"] = Output{Stdout: `"io.github.kkazuhak.e5renew" => disabled`}
	if st, _ = m.Status(); st.Enabled != No {
		t.Errorf("disabled not detected: %+v", st)
	}
	fr.Replies["launchctl print gui/501/"] = Output{ExitCode: 113}
	if st, _ = m.Status(); st.Running != No || st.Enabled != No {
		t.Errorf("unloaded not detected: %+v", st)
	}

	fr.Calls = nil
	if _, err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if got := fr.Commands(); len(got) != 1 || got[0] != "launchctl bootout gui/501/io.github.kkazuhak.e5renew" {
		t.Errorf("uninstall = %v", got)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("plist not removed")
	}
}

func TestSchtasksInstallUninstallStatus(t *testing.T) {
	o, env, fr := testOpts(t, Windows)
	env.User = `PC\alice`
	m, _ := New(env)
	if _, err := m.Install(o); err != nil {
		t.Fatal(err)
	}
	// The XML on disk is UTF-16 with a byte order mark.
	xmlPath := join(Windows, env.DataDir, "service", "e5renew-task.xml")
	b, err := os.ReadFile(strings.ReplaceAll(xmlPath, `\`, string(filepath.Separator)))
	if err != nil {
		// On a non-Windows host join() yields backslashes in the file name.
		b, err = os.ReadFile(xmlPath)
	}
	if err != nil || len(b) < 2 || b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("task xml: %v % x", err, b[:min(len(b), 4)])
	}
	got := fr.Commands()
	if len(got) != 3 || !strings.HasPrefix(got[0], "schtasks /Create /TN e5renew /XML ") || !strings.HasSuffix(got[0], " /F") ||
		got[1] != "schtasks /End /TN e5renew" || got[2] != "schtasks /Run /TN e5renew" {
		t.Errorf("commands = %v", got)
	}

	fr.Calls = nil
	fr.Replies = map[string]Output{"schtasks /Query /TN e5renew /XML": {Stdout: "<Task><Triggers><LogonTrigger><Enabled>true</Enabled></LogonTrigger></Triggers><Settings><Enabled>true</Enabled></Settings></Task>"}}
	st, err := m.Status()
	if err != nil || !st.Installed || st.Enabled != Yes || st.Running != Unknown {
		t.Fatalf("status = %+v %v", st, err)
	}
	fr.Replies["schtasks /Query /TN e5renew /XML"] = Output{Stdout: "<Task><Triggers><LogonTrigger><Enabled>true</Enabled></LogonTrigger></Triggers><Settings><Enabled>false</Enabled></Settings></Task>"}
	if st, _ = m.Status(); st.Enabled != No {
		t.Errorf("disabled task not detected: %+v", st)
	}
	fr.Replies["schtasks /Query /TN e5renew /XML"] = Output{ExitCode: 1}
	if st, _ = m.Status(); st.Installed {
		t.Errorf("missing task reported installed: %+v", st)
	}

	fr.Calls = nil
	fr.Replies = nil
	if _, err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	want := []string{"schtasks /Query /TN e5renew", "schtasks /End /TN e5renew", "schtasks /Delete /TN e5renew /F"}
	if strings.Join(fr.Commands(), "|") != strings.Join(want, "|") {
		t.Errorf("uninstall = %v", fr.Commands())
	}
}

func utf16Bytes(s string, bom bool) string {
	var b []byte
	if bom {
		b = append(b, 0xFF, 0xFE)
	}
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return string(b)
}

func TestSchtasksStatusDecodesUTF16(t *testing.T) {
	_, env, fr := testOpts(t, Windows)
	m, _ := New(env)
	doc := "<Task><Triggers><LogonTrigger><Enabled>true</Enabled></LogonTrigger></Triggers><Settings><Enabled>false</Enabled></Settings></Task>"
	for name, out := range map[string]string{"bom": utf16Bytes(doc, true), "nobom": utf16Bytes(doc, false), "utf8": doc, "utf8bom": "\ufeff" + doc} {
		fr.Replies = map[string]Output{"schtasks /Query": {Stdout: out}}
		st, err := m.Status()
		if err != nil || !st.Installed || st.Enabled != No {
			t.Errorf("%s: %+v %v", name, st, err)
		}
	}
}

func TestSchtasksQueryErrors(t *testing.T) {
	_, env, fr := testOpts(t, Windows)
	m, _ := New(env)
	// A missing task: exit status 1.
	fr.Replies = map[string]Output{"schtasks /Query": {ExitCode: 1, Stderr: "ERROR: The system cannot find the file specified."}}
	if st, err := m.Status(); err != nil || st.Installed {
		t.Errorf("missing task: %+v %v", st, err)
	}
	if res, err := m.Uninstall(); err != nil || len(res.Actions) != 1 {
		t.Errorf("uninstall of a missing task: %+v %v", res, err)
	}
	// Access denied, a crash of schtasks itself and an unknown exit code are real errors.
	for _, o := range []Output{
		{ExitCode: 1, Stderr: utf16Bytes("ERROR: Access is denied.", true)},
		{ExitCode: -1},
		{ExitCode: 5},
	} {
		fr.Replies = map[string]Output{"schtasks /Query": o}
		if _, err := m.Status(); err == nil {
			t.Errorf("%+v treated as 'not installed'", o)
		}
		if _, err := m.Uninstall(); err == nil {
			t.Errorf("uninstall: %+v treated as 'not installed'", o)
		}
	}
}

func TestSchtasksPlanRefusals(t *testing.T) {
	o, env, _ := testOpts(t, Windows)
	env.User = ""
	m, _ := New(env)
	if _, err := m.Plan(o); err == nil {
		t.Error("no user accepted")
	}
	env.User = `PC\a`
	m, _ = New(env)
	o.KeyFile = true
	if _, err := m.Plan(o); err == nil {
		t.Error("--key-file accepted on Windows")
	}
}

func TestNewRejectsUnknownPlatform(t *testing.T) {
	if _, err := New(Env{GOOS: "plan9"}); err == nil {
		t.Error("plan9 accepted")
	}
}

func runtimeIsUnix() bool { return filepath.Separator == '/' }
