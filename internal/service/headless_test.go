package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// headlessEnv returns a Windows manager whose key file is written and audited
// by fakes: package vault has no DPAPI on this host, and nothing may touch a
// real key or ACL.
func headlessEnv(t *testing.T) (InstallOptions, Env, *FakeRunner, map[string][]byte) {
	t.Helper()
	o, env, fr := testOpts(t, Windows)
	env.User = `PC\alice`
	files := map[string][]byte{}
	env.WriteKeyFile = func(path string, key []byte) error { files[path] = append([]byte(nil), key...); return nil }
	env.AuditKeyFile = func(path string) vault.KeyFileAudit {
		if _, ok := files[path]; !ok {
			return vault.KeyFileAudit{Missing: true}
		}
		return vault.KeyFileAudit{Dir: filepath.Dir(path)}
	}
	o.Headless = true
	o.MasterKey = func() ([]byte, error) { return make([]byte, 32), nil }
	return o, env, fr, files
}

func taskXMLOnDisk(t *testing.T, env Env) string {
	t.Helper()
	b, err := os.ReadFile(join(Windows, env.DataDir, "service", "e5extend-task.xml"))
	if err != nil {
		t.Fatal(err)
	}
	return decodeConsole(string(b))
}

func TestHeadlessInstallWritesKeyAndS4UTask(t *testing.T) {
	o, env, fr, files := headlessEnv(t)
	m, _ := New(env)
	res, err := m.Install(o)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := join(Windows, o.ConfigDir, vault.DPAPIKeyFileName)
	if len(files[keyPath]) != 32 {
		t.Fatalf("key file not written to %s: %v", keyPath, files)
	}
	x := taskXMLOnDisk(t, env)
	for _, want := range []string{"<LogonType>S4U</LogonType>", "<BootTrigger>", "--dpapi-key-file " + winQuote(keyPath)} {
		if !strings.Contains(x, want) {
			t.Errorf("task XML lacks %q:\n%s", want, x)
		}
	}
	if got := fr.Commands(); len(got) != 3 || !strings.HasPrefix(got[0], "schtasks /Create") {
		t.Errorf("commands = %v", got)
	}
	// The key never appears in anything printed.
	all := strings.Join(append(append(res.Actions, res.Warnings...), res.Manual...), "\n")
	if strings.Contains(all, "AAAAAAAA") {
		t.Errorf("the key leaked into the result: %s", all)
	}
	if !strings.Contains(all, "S4U") || !strings.Contains(all, "HTTPS") {
		t.Errorf("result does not explain the S4U limits: %s", all)
	}
}

func TestHeadlessPlanDoesNotComputeKey(t *testing.T) {
	o, env, _, _ := headlessEnv(t)
	o.MasterKey = func() ([]byte, error) { t.Fatal("the key was computed to print a plan"); return nil, nil }
	m, _ := New(env)
	p, err := m.Plan(o)
	if err != nil {
		t.Fatal(err)
	}
	out := p.Format()
	if !strings.Contains(out, "<secret content not shown>") || !strings.Contains(out, "DPAPI at machine scope") ||
		!strings.Contains(out, "S4U") {
		t.Errorf("plan:\n%s", out)
	}
}

func TestHeadlessRequiresKeyAndConfigDir(t *testing.T) {
	o, env, _, _ := headlessEnv(t)
	m, _ := New(env)
	bad := o
	bad.MasterKey = nil
	if _, err := m.Plan(bad); err == nil {
		t.Error("headless without a key source accepted")
	}
	bad = o
	bad.ConfigDir = ""
	if _, err := m.Plan(bad); err == nil {
		t.Error("headless without a config dir accepted")
	}
	o.KeyFile = true
	if _, err := m.Plan(o); err == nil || !strings.Contains(err.Error(), "--headless") {
		t.Errorf("--key-file on Windows: %v", err)
	}
}

func TestHeadlessRejectedOffWindows(t *testing.T) {
	for _, goos := range []string{Linux, Darwin} {
		o, env, fr := testOpts(t, goos)
		o.Headless = true
		m, _ := New(env)
		_, err := m.Plan(o)
		if err == nil || !strings.Contains(err.Error(), "only for Windows") {
			t.Fatalf("%s: %v", goos, err)
		}
		if goos == Linux && (!strings.Contains(err.Error(), "--key-file") || !strings.Contains(err.Error(), "--enable-linger")) {
			t.Errorf("the Linux message does not point to --key-file/--enable-linger: %v", err)
		}
		if _, err := m.Install(o); err == nil || len(fr.Commands()) != 0 {
			t.Errorf("%s: install ran: %v %v", goos, err, fr.Commands())
		}
	}
}

func TestHeadlessAccessDeniedPrintsElevatedCommand(t *testing.T) {
	o, env, fr, files := headlessEnv(t)
	o.Exe = `C:\tools\e5extend-bg.exe`
	fr.FailOn = []string{"schtasks /Create"}
	env.Elevated = func() bool { return false }
	m, _ := New(env)
	res, err := m.Install(o)
	if err == nil || !strings.Contains(err.Error(), "elevated") {
		t.Fatalf("err = %v", err)
	}
	if len(res.Manual) != 1 || !strings.Contains(res.Manual[0], `C:\tools\e5extend.exe --config-dir`) || !strings.HasSuffix(res.Manual[0], "service install --headless") ||
		!strings.Contains(res.Manual[0], "Run as administrator") {
		t.Errorf("manual = %v", res.Manual)
	}
	// The secret copy is not left behind for a task that was never registered.
	// (The fake writer records files in a map, so only removal on disk is
	// visible here: nothing must be reported as written.)
	for _, f := range res.Files {
		if strings.HasSuffix(f, vault.DPAPIKeyFileName) {
			t.Errorf("key file still listed: %v", res.Files)
		}
	}
	_ = files
}

func TestHeadlessElevatedCommandKeepsCustomDirs(t *testing.T) {
	got := ElevatedInstallCommand(InstallOptions{Exe: `C:\Program Files\E5\e5extend.exe`, ConfigDir: `D:\cfg`, DataDir: `D:\my data`, ExplicitDirs: true, Headless: true})
	want := `"C:\Program Files\E5\e5extend.exe" --config-dir D:\cfg --data-dir "D:\my data" service install --headless`
	if got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestHeadlessBatchRightHint(t *testing.T) {
	o, env, fr, _ := headlessEnv(t)
	fr.FailOn = []string{"schtasks /Create"}
	fr.Replies = nil
	env.Elevated = func() bool { return true }
	m, _ := New(env)
	// The fake failure text has no hint, so it is passed on unchanged.
	if _, err := m.Install(o); err == nil || strings.Contains(err.Error(), "batch job") {
		t.Errorf("unexpected hint: %v", err)
	}
	// The hint also fires on the codes, whatever language the text is in.
	for _, msg := range []string{
		"schtasks: ERROR: Logon failure: the user has not been granted the requested logon type (1385)",
		"schtasks /Create: exit status 1: FEHLER: 0x80070569",
		"schtasks: Fehler 1385",
	} {
		err := (&schtasks{env: env}).installFailure(o, &Result{}, errors.New(msg), false)
		if !strings.Contains(err.Error(), "Log on as a batch job") {
			t.Errorf("no batch-right hint for %q: %v", msg, err)
		}
	}
	// A plain install has no batch-right hint.
	plain := o
	plain.Headless = false
	if err := (&schtasks{env: env}).installFailure(plain, &Result{}, errors.New("1385"), false); strings.Contains(err.Error(), "batch") {
		t.Errorf("hint for a plain install: %v", err)
	}
}

func TestStartFailureKeepsTheKeyAndSaysHowToStart(t *testing.T) {
	o, env, fr, files := headlessEnv(t)
	fr.FailOn = []string{"schtasks /Run"}
	m, _ := New(env)
	res, err := m.Install(o)
	if err == nil || !strings.Contains(err.Error(), "registered but did not start") {
		t.Fatalf("err = %v", err)
	}
	if len(res.Manual) != 1 || !strings.Contains(res.Manual[0], "schtasks /Run /TN e5extend") {
		t.Errorf("manual = %v", res.Manual)
	}
	if len(files) != 1 {
		t.Errorf("the key file was rolled back although the task exists: %v", files)
	}
	keyPath := join(Windows, o.ConfigDir, vault.DPAPIKeyFileName)
	if !slices.Contains(res.Files, keyPath) {
		t.Errorf("key file not reported: %v", res.Files)
	}
}

func TestAccessDeniedHintInAnyMode(t *testing.T) {
	o, env, fr := testOpts(t, Windows)
	env.User = `PC\alice`
	env.Runner = deniedRunner{fr}
	fr.FailOn = []string{"schtasks /Create"}
	m, _ := New(env)
	res, err := m.Install(o)
	if err == nil || len(res.Manual) != 1 || strings.Contains(res.Manual[0], "--headless") || !strings.Contains(res.Manual[0], "service install") {
		t.Errorf("res = %+v err = %v", res, err)
	}
}

func TestPlainInstallRemovesAHeadlessKeyFile(t *testing.T) {
	o, env, _ := testOpts(t, Windows)
	env.User = `PC\alice`
	if err := os.MkdirAll(o.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := join(Windows, o.ConfigDir, vault.DPAPIKeyFileName)
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ := New(env)
	res, err := m.Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Errorf("the stale key file is still there: %v", err)
	}
	if !strings.Contains(strings.Join(res.Actions, "\n"), "removed the headless key file") {
		t.Errorf("actions = %v", res.Actions)
	}
}

func TestHeadlessTaskPassesDirectoriesExplicitly(t *testing.T) {
	o, env, _, _ := headlessEnv(t)
	m, _ := New(env)
	if _, err := m.Install(o); err != nil {
		t.Fatal(err)
	}
	x := taskXMLOnDisk(t, env)
	if !strings.Contains(x, "--config-dir") || !strings.Contains(x, "--data-dir") {
		t.Errorf("directories not passed:\n%s", x)
	}
}

const s4uTaskXML = `<Task><Triggers><BootTrigger><Enabled>true</Enabled></BootTrigger></Triggers>` +
	`<Principals><Principal><LogonType>S4U</LogonType></Principal></Principals>` +
	`<Settings><Enabled>true</Enabled></Settings>` +
	`<Actions><Exec><Arguments>--dpapi-key-file &quot;C:\Users\a b\cfg\master.key.dpapi&quot; run --log-file x</Arguments></Exec></Actions></Task>`

func TestStatusReportsHeadlessTask(t *testing.T) {
	_, env, fr, files := headlessEnv(t)
	m, _ := New(env)
	fr.Replies = map[string]Output{"schtasks /Query /TN e5extend /XML": {Stdout: s4uTaskXML}}

	// Key file missing.
	st, err := m.Status()
	if err != nil || !st.Installed || st.Headless != Yes || st.Running != Unknown || st.Enabled != Yes {
		t.Fatalf("status = %+v %v", st, err)
	}
	if st.KeyFile != `C:\Users\a b\cfg\master.key.dpapi` || !st.KeyFileMissing {
		t.Errorf("key file state = %+v", st)
	}
	// Present and clean.
	files[st.KeyFile] = make([]byte, 32)
	st, _ = m.Status()
	if st.KeyFileMissing || st.KeyFileErr != "" || len(st.KeyFileBroad) != 0 {
		t.Errorf("clean key file reported as a problem: %+v", st)
	}
	// Unreadable and too open.
	env.AuditKeyFile = func(string) vault.KeyFileAudit {
		return vault.KeyFileAudit{ReadErr: "Access is denied.", Broad: []string{`BUILTIN\Users`}}
	}
	m, _ = New(env)
	st, _ = m.Status()
	if st.KeyFileErr == "" || len(st.KeyFileBroad) != 1 {
		t.Errorf("problems not reported: %+v", st)
	}
	if d := strings.Join(st.Details, "\n"); !strings.Contains(d, `BUILTIN\Users`) || strings.Contains(d, "Users\\a b") {
		t.Errorf("details = %s", d)
	}
}

func TestStatusInteractiveTaskIsNotHeadless(t *testing.T) {
	_, env, fr := testOpts(t, Windows)
	m, _ := New(env)
	fr.Replies = map[string]Output{"schtasks /Query": {Stdout: "<Task><Principals><Principal><LogonType>InteractiveToken</LogonType></Principal></Principals><Settings><Enabled>true</Enabled></Settings></Task>"}}
	st, err := m.Status()
	if err != nil || st.Headless != No || st.KeyFile != "" || st.Running != Unknown {
		t.Errorf("status = %+v %v", st, err)
	}
}

func TestUninstallRemovesOnlyAHeadlessKeyFile(t *testing.T) {
	dir := t.TempDir()
	key := dir + string(os.PathSeparator) + vault.DPAPIKeyFileName
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	xml := strings.Replace(s4uTaskXML, `C:\Users\a b\cfg\master.key.dpapi`, key, 1)

	// An interactive task over a leftover key file: the file only ever comes
	// from --headless, so it goes too, found next to the configuration.
	_, env, fr := testOpts(t, Windows)
	env.ConfigDir = filepath.Join(dir, "cfg")
	if err := os.MkdirAll(env.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := join(Windows, env.ConfigDir, vault.DPAPIKeyFileName)
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ := New(env)
	fr.Replies = map[string]Output{"schtasks /Query": {Stdout: "<Task><Principals><Principal><LogonType>InteractiveToken</LogonType></Principal></Principals></Task>"}}
	if _, err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("an interactive uninstall left the key file: %v", err)
	}
	// With no task at all, a stray key file is removed as well.
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fr.Replies = map[string]Output{"schtasks /Query": {ExitCode: 1, Stderr: "ERROR: The system cannot find the file specified."}}
	if _, err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a stray key file was kept: %v", err)
	}

	// A headless task: the key file goes, and the message says what stays.
	fr.Replies = map[string]Output{"schtasks /Query": {Stdout: xml}}
	res, err := m.Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Errorf("key file kept: %v", err)
	}
	if w := strings.Join(res.Warnings, "\n"); !strings.Contains(w, "left in place") {
		t.Errorf("warnings = %v", res.Warnings)
	}

	// A task that points at some other file never deletes it.
	other := dir + string(os.PathSeparator) + "precious.txt"
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fr.Replies = map[string]Output{"schtasks /Query": {Stdout: strings.Replace(xml, key, other, 1)}}
	res, _ = m.Uninstall()
	if _, err := os.Stat(other); err != nil {
		t.Errorf("deleted a file that is not the key file: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Error("no warning about the foreign path")
	}
}

func TestUninstallAccessDeniedGivesElevatedCommand(t *testing.T) {
	_, env, fr := testOpts(t, Windows)
	fr.Replies = map[string]Output{"schtasks /Query": {Stdout: s4uTaskXML}}
	fr.FailOn = []string{"schtasks /Delete"}
	env.Runner = deniedRunner{fr}
	m, _ := New(env)
	res, err := m.Uninstall()
	if err == nil || len(res.Manual) != 1 || !strings.Contains(res.Manual[0], "schtasks /Delete /TN e5extend /F") {
		t.Errorf("res = %+v err = %v", res, err)
	}
}

// deniedRunner makes a failed command say what schtasks says without rights.
type deniedRunner struct{ *FakeRunner }

func (r deniedRunner) Run(ctx context.Context, name string, args ...string) (Output, error) {
	out, err := r.FakeRunner.Run(ctx, name, args...)
	if err != nil {
		err = errors.New("schtasks: exit status 1: ERROR: Access is denied.")
	}
	return out, err
}
