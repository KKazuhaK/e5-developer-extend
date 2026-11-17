package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tarGz(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// An unrelated entry first, and the program inside a folder, as the real archives may have.
	for _, e := range []struct{ name, body string }{{"README.md", "readme"}, {"pkg/" + name, content}} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(e.body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, _ := zw.Create(n)
		_, _ = w.Write([]byte(c))
	}
	_ = zw.Close()
	return buf.Bytes()
}

// fixture is a fake release server with signed checksums.
type fixture struct {
	srv     *httptest.Server
	rel     Release
	key     SecretKey
	hits    atomic.Int32
	served  map[string][]byte // what the server really sends (may be tampered)
	TempDir string
}

type fixtureOpts struct {
	noSig  bool
	tamper map[string][]byte // name -> bytes the server sends instead of the signed ones
	other  bool              // sign with a key that is not the one the installer trusts
}

func newFixture(t *testing.T, tag string, files map[string][]byte, o fixtureOpts) *fixture {
	t.Helper()
	f := &fixture{key: testKey(t), served: map[string][]byte{}, TempDir: t.TempDir()}
	var sums strings.Builder
	names := make([]string, 0, len(files))
	for n, b := range files {
		h := sha256.Sum256(b)
		sums.WriteString(hex.EncodeToString(h[:]) + "  " + n + "\n")
		f.served[n] = b
		names = append(names, n)
	}
	for n, b := range o.tamper {
		f.served[n] = b
	}
	f.served[SumsName] = []byte(sums.String())
	names = append(names, SumsName)
	if !o.noSig {
		k := f.key
		if o.other {
			k = testKey(t)
		}
		f.served[SigName] = k.Sign([]byte(sums.String()), "file:SHA256SUMS")
		names = append(names, SigName)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		b, ok := f.served[strings.TrimPrefix(r.URL.Path, "/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(f.srv.Close)
	f.rel = Release{Tag: tag}
	for _, n := range names {
		f.rel.Assets = append(f.rel.Assets, Asset{Name: n, URL: f.srv.URL + "/dl/" + n, Size: int64(len(f.served[n]))})
	}
	return f
}

func (f *fixture) installer(goos, goarch string) *Installer {
	return &Installer{
		PublicKey: f.key.Public().String(), GOOS: goos, GOARCH: goarch, TempRoot: f.TempDir,
		Downloader: &Downloader{Allow: func(string) bool { return true }},
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func leftovers(t *testing.T, root string) []string {
	t.Helper()
	ents, _ := os.ReadDir(root)
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), workPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func writeExe(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInstallLinuxStandalone(t *testing.T) {
	f := newFixture(t, "v0.3.0", map[string][]byte{"e5extend_0.3.0_linux_amd64.tar.gz": tarGz(t, "e5extend", "NEW PROGRAM")}, fixtureOpts{})
	bin := t.TempDir()
	exe := filepath.Join(bin, "e5extend")
	writeExe(t, exe, "OLD PROGRAM")

	var progress []Progress
	i := f.installer("linux", "amd64")
	i.Progress = func(p Progress) { progress = append(progress, p) }
	out, err := i.Install(context.Background(), f.rel, Installation{Kind: KindLinuxStandalone, Exe: exe, Dir: bin}, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if read(t, exe) != "NEW PROGRAM" {
		t.Fatal("the program was not replaced")
	}
	if fi, _ := os.Stat(exe); runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("the new program is not executable: %v", fi.Mode())
	}
	if !out.Done || !out.RestartService || out.ExitNow || out.Version != "0.3.0" || len(out.Manual) != 0 {
		t.Fatalf("outcome: %+v", out)
	}
	if got := leftovers(t, f.TempDir); len(got) != 0 {
		t.Fatalf("working folder left behind: %v", got)
	}
	if entries, _ := os.ReadDir(bin); len(entries) != 1 {
		t.Fatalf("stray files next to the program: %v", entries)
	}
	var sawDownload, sawVerify, sawInstall bool
	for _, p := range progress {
		sawDownload = sawDownload || p.Stage == "download" && p.File != ""
		sawVerify = sawVerify || p.Stage == "verify"
		sawInstall = sawInstall || p.Stage == "install"
	}
	if !sawDownload || !sawVerify || !sawInstall {
		t.Fatalf("progress stages: %+v", progress)
	}
}

func TestInstallRefusals(t *testing.T) {
	name := "e5extend_0.3.0_linux_amd64.tar.gz"
	files := map[string][]byte{name: tarGz(t, "e5extend", "NEW")}
	inst := func(exe, dir string) Installation { return Installation{Kind: KindLinuxStandalone, Exe: exe, Dir: dir} }

	t.Run("checksum mismatch", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{tamper: map[string][]byte{name: tarGz(t, "e5extend", "EVIL")}})
		bin := t.TempDir()
		exe := filepath.Join(bin, "e5extend")
		writeExe(t, exe, "OLD")
		_, err := f.installer("linux", "amd64").Install(context.Background(), f.rel, inst(exe, bin), "0.2.0")
		if !errors.Is(err, ErrChecksum) {
			t.Fatalf("err = %v", err)
		}
		if read(t, exe) != "OLD" || len(leftovers(t, f.TempDir)) != 0 {
			t.Fatal("a tampered download touched the installation or left files")
		}
	})
	t.Run("no signature in the release", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{noSig: true})
		bin := t.TempDir()
		exe := filepath.Join(bin, "e5extend")
		writeExe(t, exe, "OLD")
		_, err := f.installer("linux", "amd64").Install(context.Background(), f.rel, inst(exe, bin), "0.2.0")
		if !errors.Is(err, ErrNoSignature) || f.hits.Load() != 0 || read(t, exe) != "OLD" {
			t.Fatalf("err=%v downloads=%d", err, f.hits.Load())
		}
	})
	t.Run("signed by another key", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{other: true})
		bin := t.TempDir()
		exe := filepath.Join(bin, "e5extend")
		writeExe(t, exe, "OLD")
		_, err := f.installer("linux", "amd64").Install(context.Background(), f.rel, inst(exe, bin), "0.2.0")
		if !errors.Is(err, ErrBadSignature) || read(t, exe) != "OLD" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("placeholder key", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{})
		bin := t.TempDir()
		exe := filepath.Join(bin, "e5extend")
		writeExe(t, exe, "OLD")
		i := f.installer("linux", "amd64")
		i.PublicKey = placeholderKey
		_, err := i.Install(context.Background(), f.rel, inst(exe, bin), "0.2.0")
		var me *ManualError
		if !errors.As(err, &me) || me.Plan.Reason != ReasonNoKey || f.hits.Load() != 0 {
			t.Fatalf("err=%v downloads=%d", err, f.hits.Load())
		}
	})
	t.Run("not newer", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{})
		for _, cur := range []string{"0.3.0", "0.4.0", "dev"} {
			if _, err := f.installer("linux", "amd64").Install(context.Background(), f.rel, inst("/x/e5extend", "/x"), cur); err == nil {
				t.Errorf("installed 0.3.0 over %s", cur)
			}
		}
		if f.hits.Load() != 0 {
			t.Fatal("downloaded for nothing")
		}
	})
	t.Run("docker", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{})
		_, err := f.installer("linux", "amd64").Install(context.Background(), f.rel, Installation{Kind: KindDocker}, "0.2.0")
		var me *ManualError
		if !errors.As(err, &me) || me.Plan.Reason != ReasonDocker {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("release lacks the file for this system", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{})
		_, err := f.installer("linux", "arm64").Install(context.Background(), f.rel, inst("/x/e5extend", "/x"), "0.2.0")
		if err == nil || !strings.Contains(err.Error(), "e5extend_0.3.0_linux_arm64.tar.gz") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("download from a foreign address", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{})
		i := f.installer("linux", "amd64")
		i.Downloader = &Downloader{} // the real rule: only the project's release address
		bin := t.TempDir()
		_, err := i.Install(context.Background(), f.rel, inst(filepath.Join(bin, "e5extend"), bin), "0.2.0")
		if err == nil || !strings.Contains(err.Error(), "refusing") || f.hits.Load() != 0 {
			t.Fatalf("err=%v downloads=%d", err, f.hits.Load())
		}
	})
}

func TestInstallLinuxStandaloneNotWritable(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("root can write anywhere, and Windows has no read-only folders in this sense")
	}
	f := newFixture(t, "v0.3.0", map[string][]byte{"e5extend_0.3.0_linux_amd64.tar.gz": tarGz(t, "e5extend", "NEW")}, fixtureOpts{})
	bin := t.TempDir()
	exe := filepath.Join(bin, "e5extend")
	writeExe(t, exe, "OLD")
	if err := os.Chmod(bin, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bin, 0o755) })
	out, err := f.installer("linux", "amd64").Install(context.Background(), f.rel, Installation{Kind: KindLinuxStandalone, Exe: exe, Dir: bin}, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if out.Done || len(out.Manual) != 1 || !strings.HasPrefix(out.Manual[0], "sudo install -m 0755 ") || !strings.Contains(out.Manual[0], exe) {
		t.Fatalf("outcome: %+v", out)
	}
	if read(t, exe) != "OLD" || out.KeepDir == "" {
		t.Fatal("must leave the program alone and keep the download for the manual step")
	}
	if _, err := os.Stat(filepath.Join(out.KeepDir, "unpacked", "e5extend")); err != nil {
		t.Fatalf("the unpacked program the manual command refers to is missing: %v", err)
	}
}

func TestInstallWindowsCLIMovesProgramsAside(t *testing.T) {
	zipName := "e5extend_0.3.0_windows_amd64.zip"
	f := newFixture(t, "v0.3.0", map[string][]byte{zipName: zipOf(t, map[string]string{"e5extend.exe": "NEW CLI", "e5extend-bg.exe": "NEW BG", "LICENSE": "x"})}, fixtureOpts{})
	dir := t.TempDir()
	writeExe(t, filepath.Join(dir, "e5extend.exe"), "OLD CLI")
	writeExe(t, filepath.Join(dir, "e5extend-bg.exe"), "OLD BG")
	out, err := f.installer("windows", "amd64").Install(context.Background(), f.rel,
		Installation{Kind: KindWindowsCLI, Exe: filepath.Join(dir, "e5extend.exe"), Dir: dir}, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "e5extend.exe")) != "NEW CLI" || read(t, filepath.Join(dir, "e5extend-bg.exe")) != "NEW BG" {
		t.Fatal("programs not replaced")
	}
	// The running programs were moved, not deleted: that is what Windows allows.
	if read(t, filepath.Join(dir, "e5extend.exe.old")) != "OLD CLI" || read(t, filepath.Join(dir, "e5extend-bg.exe.old")) != "OLD BG" {
		t.Fatal("the old programs were not kept aside")
	}
	if !out.Done || !out.RestartService {
		t.Fatalf("outcome: %+v", out)
	}
	CleanupAside(dir)
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Fatalf("aside files not cleaned: %v", ents)
	}
}

func TestInstallWindowsCLIOnlyReplacesWhatIsThere(t *testing.T) {
	zipName := "e5extend_0.3.0_windows_amd64.zip"
	f := newFixture(t, "v0.3.0", map[string][]byte{zipName: zipOf(t, map[string]string{"e5extend.exe": "NEW CLI", "e5extend-bg.exe": "NEW BG"})}, fixtureOpts{})
	dir := t.TempDir()
	writeExe(t, filepath.Join(dir, "e5extend.exe"), "OLD CLI")
	if _, err := f.installer("windows", "amd64").Install(context.Background(), f.rel,
		Installation{Kind: KindWindowsCLI, Exe: filepath.Join(dir, "e5extend.exe"), Dir: dir}, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "e5extend-bg.exe")); err == nil {
		t.Fatal("created a program that was not installed")
	}
}

func TestReplaceFileAsideRollsBack(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "e5extend.exe")
	writeExe(t, dst, "OLD")
	err := replaceFile(dst, filepath.Join(dir, "does-not-exist"), true)
	if err == nil {
		t.Fatal("expected an error")
	}
	if read(t, dst) != "OLD" {
		t.Fatal("the old program was not put back")
	}
	if _, err := os.Stat(dst + ".old"); err == nil {
		t.Fatal("aside file left after the rollback")
	}
}

func TestInstallWindowsSetupStartsInstallerAndExits(t *testing.T) {
	setup := "E5-Developer-Extend_0.3.0_windows_amd64_setup.exe"
	f := newFixture(t, "v0.3.0", map[string][]byte{setup: []byte("MZ installer")}, fixtureOpts{})
	var started []string
	i := f.installer("windows", "amd64")
	i.StartDetached = func(name string, args ...string) error {
		started = append(append([]string{name}, args...), "")
		if read(t, name) != "MZ installer" {
			t.Error("the installer was started before it was in place")
		}
		return nil
	}
	out, err := i.Install(context.Background(), f.rel, Installation{Kind: KindWindowsSetup, Exe: `C:\x\e5extend-gui.exe`, Dir: `C:\x`}, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(started) != 3 || filepath.Base(started[0]) != setup || started[1] != "/S" {
		t.Fatalf("started %v", started)
	}
	if !out.ExitNow || !out.Done {
		t.Fatalf("outcome: %+v", out)
	}
	// The setup runs from the working folder, so it is kept (the next upgrade cleans old ones).
	if got := leftovers(t, f.TempDir); len(got) != 1 {
		t.Fatalf("working folders: %v", got)
	}
}

func TestInstallDebPackages(t *testing.T) {
	cli, desk := "e5extend_0.3.0-1_amd64.deb", "e5extend-desktop_0.3.0-1_amd64.deb"
	files := map[string][]byte{cli: []byte("deb1"), desk: []byte("deb2")}
	in := Installation{Kind: KindLinuxDeb, Exe: "/usr/bin/e5extend-gui", Dir: "/usr/bin", Desktop: true}

	t.Run("root installs both packages in one transaction", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{})
		var cmds []string
		i := f.installer("linux", "amd64")
		i.Geteuid = func() int { return 0 }
		i.Run = func(_ context.Context, name string, args ...string) (string, error) {
			cmds = append(cmds, name+" "+strings.Join(args, " "))
			return "", nil
		}
		out, err := i.Install(context.Background(), f.rel, in, "0.2.0")
		if err != nil {
			t.Fatal(err)
		}
		if len(cmds) != 1 || !strings.HasPrefix(cmds[0], "env DEBIAN_FRONTEND=noninteractive apt-get install -y ") ||
			!strings.Contains(cmds[0], cli) || !strings.Contains(cmds[0], desk) {
			t.Fatalf("commands: %v", cmds)
		}
		if !out.Done || !out.RestartService || !out.RestartApp {
			t.Fatalf("outcome: %+v", out)
		}
	})
	t.Run("a graphical session asks for rights with pkexec", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{})
		var cmds []string
		i := f.installer("linux", "amd64")
		i.Geteuid = func() int { return 1000 }
		i.UsePkexec = true
		i.LookPath = func(n string) (string, error) {
			if n == "pkexec" {
				return "/usr/bin/pkexec", nil
			}
			return "", errors.New("not found")
		}
		i.Run = func(_ context.Context, name string, args ...string) (string, error) {
			cmds = append(cmds, name+" "+strings.Join(args, " "))
			return "", nil
		}
		if _, err := i.Install(context.Background(), f.rel, in, "0.2.0"); err != nil {
			t.Fatal(err)
		}
		if len(cmds) != 1 || !strings.HasPrefix(cmds[0], "pkexec env DEBIAN_FRONTEND=noninteractive apt-get install -y ") {
			t.Fatalf("commands: %v", cmds)
		}
	})
	t.Run("without rights the exact sudo command is given", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{})
		i := f.installer("linux", "amd64")
		i.Geteuid = func() int { return 1000 }
		i.Run = func(context.Context, string, ...string) (string, error) {
			t.Fatal("ran a command without rights")
			return "", nil
		}
		out, err := i.Install(context.Background(), f.rel, in, "0.2.0")
		if err != nil {
			t.Fatal(err)
		}
		if out.Done || len(out.Manual) != 1 || !strings.HasPrefix(out.Manual[0], "sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y ") {
			t.Fatalf("outcome: %+v", out)
		}
		// The files named in the command exist, in a folder that was kept.
		for _, n := range []string{cli, desk} {
			if _, err := os.Stat(filepath.Join(out.KeepDir, n)); err != nil {
				t.Fatalf("%s: %v", n, err)
			}
		}
	})
	t.Run("a failing package manager is reported with its output", func(t *testing.T) {
		f := newFixture(t, "v0.3.0", files, fixtureOpts{})
		i := f.installer("linux", "amd64")
		i.Geteuid = func() int { return 0 }
		i.Run = func(context.Context, string, ...string) (string, error) {
			return "E: Unable to locate package", errors.New("exit status 100")
		}
		_, err := i.Install(context.Background(), f.rel, in, "0.2.0")
		if err == nil || !strings.Contains(err.Error(), "Unable to locate package") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestInstallRPMUsesDnf(t *testing.T) {
	rpm := "e5extend-0.3.0-1.x86_64.rpm"
	f := newFixture(t, "v0.3.0", map[string][]byte{rpm: []byte("rpm")}, fixtureOpts{})
	var cmd string
	i := f.installer("linux", "amd64")
	i.Geteuid = func() int { return 0 }
	i.LookPath = func(n string) (string, error) {
		if n == "dnf" {
			return "/usr/bin/dnf", nil
		}
		return "", errors.New("no")
	}
	i.Run = func(_ context.Context, name string, args ...string) (string, error) {
		cmd = name + " " + strings.Join(args, " ")
		return "", nil
	}
	out, err := i.Install(context.Background(), f.rel, Installation{Kind: KindLinuxRPM, Exe: "/usr/bin/e5extend", Dir: "/usr/bin"}, "0.2.0")
	if err != nil || !strings.HasPrefix(cmd, "dnf install -y ") || !strings.HasSuffix(cmd, rpm) || out.RestartApp {
		t.Fatalf("cmd=%q out=%+v err=%v", cmd, out, err)
	}
}

func TestInstallMacApp(t *testing.T) {
	dmg := "E5-Developer-Extend_0.3.0_macos_universal.dmg"
	f := newFixture(t, "v0.3.0", map[string][]byte{dmg: []byte("dmg bytes")}, fixtureOpts{})
	apps := t.TempDir()
	app := filepath.Join(apps, "E5 Developer Extend.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(app, "Contents", "MacOS", "e5extend"), "OLD")

	var cmds []string
	i := f.installer("darwin", "arm64")
	i.Run = func(_ context.Context, name string, args ...string) (string, error) {
		cmds = append(cmds, name+" "+args[0])
		switch name {
		case "hdiutil":
			if args[0] == "attach" {
				mnt := args[len(args)-2]
				newApp := filepath.Join(mnt, "E5 Developer Extend.app", "Contents", "MacOS")
				_ = os.MkdirAll(newApp, 0o755)
				_ = os.WriteFile(filepath.Join(newApp, "e5extend"), []byte("NEW"), 0o755)
			}
		case "ditto":
			return "", os.CopyFS(args[1], os.DirFS(args[0]))
		}
		return "", nil
	}
	out, err := i.Install(context.Background(), f.rel, Installation{Kind: KindMacApp, AppPath: app, Exe: filepath.Join(app, "Contents", "MacOS", "e5extend-gui"), Dir: filepath.Join(app, "Contents", "MacOS")}, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(app, "Contents", "MacOS", "e5extend")) != "NEW" {
		t.Fatal("the app was not replaced")
	}
	if ents, _ := os.ReadDir(apps); len(ents) != 1 {
		t.Fatalf("leftovers next to the app: %v", ents)
	}
	if !out.Done || !out.RestartApp || !out.RestartService {
		t.Fatalf("outcome: %+v", out)
	}
	if len(cmds) != 3 || cmds[0] != "hdiutil attach" {
		t.Fatalf("commands: %v", cmds)
	}
	if cmds[len(cmds)-1] != "hdiutil detach" {
		t.Fatalf("the disk image was not detached: %v", cmds)
	}
}

func TestInstallMacAppFolderNotWritable(t *testing.T) {
	dmg := "E5-Developer-Extend_0.3.0_macos_universal.dmg"
	f := newFixture(t, "v0.3.0", map[string][]byte{dmg: []byte("dmg")}, fixtureOpts{})
	i := f.installer("darwin", "arm64")
	i.Run = func(context.Context, string, ...string) (string, error) { t.Fatal("ran a command"); return "", nil }
	out, err := i.Install(context.Background(), f.rel, Installation{Kind: KindMacApp, AppPath: "/nonexistent-folder/E5 Developer Extend.app"}, "0.2.0")
	if err != nil || out.Done || len(out.Manual) != 1 || out.KeepDir == "" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestDownloaderLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("short")) }))
	defer srv.Close()
	d := &Downloader{Allow: func(string) bool { return true }}
	dir := t.TempDir()
	// The announced size does not match what arrives.
	if _, err := d.Fetch(context.Background(), Asset{Name: "a.bin", URL: srv.URL, Size: 5}, dir, nil); err != nil {
		t.Fatalf("matching size: %v", err)
	}
	for _, name := range []string{"../evil", `a\b`, "", ".."} {
		if _, err := d.Fetch(context.Background(), Asset{Name: name, URL: srv.URL}, dir, nil); err == nil {
			t.Errorf("accepted the file name %q", name)
		}
	}
	if _, err := d.Fetch(context.Background(), Asset{Name: "big.bin", URL: srv.URL, Size: maxDownload + 1}, dir, nil); err == nil {
		t.Error("accepted an oversize file")
	}
}

func TestDownloaderStallAndCap(t *testing.T) {
	// A server that sends a few bytes and then nothing.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("abc"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	dir := t.TempDir()
	d := &Downloader{Allow: func(string) bool { return true }, Stall: 200 * time.Millisecond}
	start := time.Now()
	_, err := d.Fetch(context.Background(), Asset{Name: "a.bin", URL: srv.URL}, dir, nil)
	if err == nil || !strings.Contains(err.Error(), "no data received") || time.Since(start) > 5*time.Second {
		t.Fatalf("stalled download: %v after %v", err, time.Since(start))
	}
	if _, serr := os.Stat(filepath.Join(dir, "a.bin")); serr == nil {
		t.Error("the partial file was left")
	}
	// A slow but steady download is stopped by the overall cap.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for r.Context().Err() == nil {
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer slow.Close()
	d = &Downloader{Allow: func(string) bool { return true }, Stall: time.Second, MaxTime: 300 * time.Millisecond}
	if _, err = d.Fetch(context.Background(), Asset{Name: "b.bin", URL: slow.URL}, dir, nil); err == nil || !strings.Contains(err.Error(), "took longer") {
		t.Fatalf("overall cap: %v", err)
	}
	// A deadline of the caller is not the cap, and a cancel is neither.
	d = &Downloader{Allow: func(string) bool { return true }, Stall: time.Second, MaxTime: time.Minute}
	cctx, ccancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer ccancel()
	if _, err = d.Fetch(cctx, Asset{Name: "c.bin", URL: slow.URL}, dir, nil); err == nil || !strings.Contains(err.Error(), "time limit of the request") || strings.Contains(err.Error(), "took longer") {
		t.Fatalf("caller deadline: %v", err)
	}
	kctx, kcancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, kcancel)
	if _, err = d.Fetch(kctx, Asset{Name: "d.bin", URL: slow.URL}, dir, nil); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("caller cancel: %v", err)
	}
}

func TestCleanOldWorkDirs(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, workPrefix+"old")
	newDir := filepath.Join(root, workPrefix+"new")
	other := filepath.Join(root, "unrelated")
	for _, d := range []string{oldDir, newDir, other} {
		_ = os.Mkdir(d, 0o700)
	}
	past := time.Now().AddDate(0, 0, -2)
	_ = os.Chtimes(oldDir, past, past)
	_ = os.Chtimes(other, past, past)
	cleanOldWorkDirs(root)
	for d, want := range map[string]bool{oldDir: false, newDir: true, other: true} {
		if _, err := os.Stat(d); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(d), err == nil, want)
		}
	}
}

func TestInstallWindowsCLIUndoesTheFirstSwapWhenTheSecondFails(t *testing.T) {
	zipName := "e5extend_0.3.0_windows_amd64.zip"
	f := newFixture(t, "v0.3.0", map[string][]byte{zipName: zipOf(t, map[string]string{"e5extend.exe": "NEW CLI", "e5extend-bg.exe": "NEW BG"})}, fixtureOpts{})
	dir := t.TempDir()
	writeExe(t, filepath.Join(dir, "e5extend.exe"), "OLD CLI")
	writeExe(t, filepath.Join(dir, "e5extend-bg.exe"), "OLD BG")
	// A non-empty folder where the second program would be moved aside: that swap fails.
	blocker := filepath.Join(dir, "e5extend-bg.exe"+asideSuffix)
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := f.installer("windows", "amd64").Install(context.Background(), f.rel,
		Installation{Kind: KindWindowsCLI, Exe: filepath.Join(dir, "e5extend.exe"), Dir: dir}, "0.2.0")
	if err == nil {
		t.Fatal("expected the second swap to fail")
	}
	if read(t, filepath.Join(dir, "e5extend.exe")) != "OLD CLI" || read(t, filepath.Join(dir, "e5extend-bg.exe")) != "OLD BG" {
		t.Fatal("a half-upgraded installation was left behind")
	}
	if _, err := os.Stat(filepath.Join(dir, "e5extend.exe"+asideSuffix)); err == nil {
		t.Fatal("the first program's aside copy was not put back")
	}
}

func TestPlanForRefusesToReplaceAnotherProgramWithTheArchivesBinary(t *testing.T) {
	i := &Installer{PublicKey: testKeyText(t), GOOS: "linux", GOARCH: "amd64"}
	for _, exe := range []string{"/opt/x/e5extend-gui", "/opt/x/my-e5"} {
		p := i.PlanFor(Installation{Kind: KindLinuxStandalone, Exe: exe}, "0.2.0")
		if p.Mode != ModeManual || p.Reason != ReasonUnsupported {
			t.Errorf("%s: %+v", exe, p)
		}
	}
	if p := i.PlanFor(Installation{Kind: KindLinuxStandalone, Exe: "/opt/x/e5extend"}, "0.2.0"); p.Mode != ModeAuto {
		t.Errorf("the program itself: %+v", p)
	}
	if p := i.PlanFor(Installation{Kind: KindMacCLI, Exe: "/usr/local/bin/e5extend"}, "0.2.0"); p.Mode != ModeAuto {
		t.Errorf("mac: %+v", p)
	}
}
