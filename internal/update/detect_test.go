package update

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// machine describes a fake computer for Detect.
type machine struct {
	files map[string]bool
	env   map[string]string
	owned map[string]string // command line -> output
}

func (m machine) Env(goos, goarch, exe string) Env {
	return Env{
		GOOS: goos, GOARCH: goarch, Exe: exe,
		Getenv: func(k string) string { return m.env[k] },
		Exists: func(p string) bool { return m.files[p] },
		Run: func(_ context.Context, name string, args ...string) (string, error) {
			if out, ok := m.owned[name+" "+strings.Join(args, " ")]; ok {
				return out, nil
			}
			return "", errors.New("exit status 1")
		},
	}
}

func TestDetect(t *testing.T) {
	winDir := `C:\Users\me\AppData\Local\Programs\E5 Developer Extend`
	cases := []struct {
		name        string
		m           machine
		goos, exe   string
		kind        Kind
		app         string
		desktopWant bool
	}{
		{"windows installer (uninstall.exe next to it)", machine{files: map[string]bool{winDir + `\uninstall.exe`: true}}, "windows", winDir + `\e5extend.exe`, KindWindowsSetup, "", false},
		{"windows installer (the default folder)", machine{env: map[string]string{"LOCALAPPDATA": `C:\Users\me\AppData\Local`}}, "windows", strings.ToLower(winDir) + `\e5extend-gui.exe`, KindWindowsSetup, "", false},
		{"windows zip", machine{env: map[string]string{"LOCALAPPDATA": `C:\Users\me\AppData\Local`}}, "windows", `D:\tools\e5extend\e5extend.exe`, KindWindowsCLI, "", false},
		{"macOS app", machine{}, "darwin", "/Applications/E5 Developer Extend.app/Contents/MacOS/e5extend-gui", KindMacApp, "/Applications/E5 Developer Extend.app", false},
		{"macOS app in ~/Applications", machine{}, "darwin", "/Users/me/Applications/E5 Developer Extend.app/Contents/MacOS/e5extend", KindMacApp, "/Users/me/Applications/E5 Developer Extend.app", false},
		{"macOS tarball", machine{}, "darwin", "/usr/local/bin/e5extend", KindMacCLI, "", false},
		{"docker (dockerenv)", machine{files: map[string]bool{"/.dockerenv": true}}, "linux", "/usr/bin/e5extend", KindDocker, "", false},
		{"docker (env)", machine{env: map[string]string{"E5EXTEND_DOCKER": "1"}}, "linux", "/usr/local/bin/e5extend", KindDocker, "", false},
		{"deb, command line only", machine{owned: map[string]string{"dpkg -S /usr/bin/e5extend": "e5extend: /usr/bin/e5extend"}}, "linux", "/usr/bin/e5extend", KindLinuxDeb, "", false},
		{"deb with desktop", machine{files: map[string]bool{"/usr/bin/e5extend-gui": true}, owned: map[string]string{"dpkg -S /usr/bin/e5extend-gui": "e5extend-desktop: /usr/bin/e5extend-gui"}}, "linux", "/usr/bin/e5extend-gui", KindLinuxDeb, "", true},
		{"rpm with desktop", machine{files: map[string]bool{"/usr/bin/e5extend-gui": true}, owned: map[string]string{"rpm -qf /usr/bin/e5extend": "e5extend-0.2.0-1.x86_64"}}, "linux", "/usr/bin/e5extend", KindLinuxRPM, "", true},
		{"standalone tarball", machine{}, "linux", "/home/me/bin/e5extend", KindLinuxStandalone, "", false},
		{"built from source in /usr/local", machine{}, "linux", "/usr/local/bin/e5extend", KindLinuxStandalone, "", false},
	}
	for _, c := range cases {
		in := Detect(context.Background(), c.m.Env(c.goos, "amd64", c.exe))
		if in.Kind != c.kind || in.AppPath != c.app || in.Desktop != c.desktopWant {
			t.Errorf("%s: got %+v, want kind %s app %q desktop %v", c.name, in, c.kind, c.app, c.desktopWant)
		}
	}
	if in := Detect(context.Background(), Env{GOOS: "linux"}); in.Kind != KindUnknown {
		t.Errorf("no executable: %+v", in)
	}
}

func TestAssetsMatrix(t *testing.T) {
	const v = "0.3.0"
	type row struct {
		in           Installation
		goos, goarch string
		want         []string
	}
	rows := []row{
		{Installation{Kind: KindWindowsSetup}, "windows", "amd64", []string{"E5-Developer-Extend_0.3.0_windows_amd64_setup.exe"}},
		{Installation{Kind: KindWindowsSetup}, "windows", "arm64", []string{"E5-Developer-Extend_0.3.0_windows_arm64_setup.exe"}},
		{Installation{Kind: KindWindowsCLI}, "windows", "amd64", []string{"e5extend_0.3.0_windows_amd64.zip"}},
		{Installation{Kind: KindWindowsCLI}, "windows", "arm64", []string{"e5extend_0.3.0_windows_arm64.zip"}},
		{Installation{Kind: KindMacApp}, "darwin", "arm64", []string{"E5-Developer-Extend_0.3.0_macos_universal.dmg"}},
		{Installation{Kind: KindMacApp}, "darwin", "amd64", []string{"E5-Developer-Extend_0.3.0_macos_universal.dmg"}},
		{Installation{Kind: KindMacCLI}, "darwin", "arm64", []string{"e5extend_0.3.0_darwin_universal.tar.gz"}},
		{Installation{Kind: KindLinuxStandalone}, "linux", "amd64", []string{"e5extend_0.3.0_linux_amd64.tar.gz"}},
		{Installation{Kind: KindLinuxStandalone}, "linux", "arm64", []string{"e5extend_0.3.0_linux_arm64.tar.gz"}},
		{Installation{Kind: KindLinuxDeb}, "linux", "amd64", []string{"e5extend_0.3.0-1_amd64.deb"}},
		{Installation{Kind: KindLinuxDeb, Desktop: true}, "linux", "arm64", []string{"e5extend_0.3.0-1_arm64.deb", "e5extend-desktop_0.3.0-1_arm64.deb"}},
		{Installation{Kind: KindLinuxRPM}, "linux", "amd64", []string{"e5extend-0.3.0-1.x86_64.rpm"}},
		{Installation{Kind: KindLinuxRPM, Desktop: true}, "linux", "arm64", []string{"e5extend-0.3.0-1.aarch64.rpm", "e5extend-desktop-0.3.0-1.aarch64.rpm"}},
	}
	for _, r := range rows {
		got, err := Assets(r.in, r.goos, r.goarch, v)
		if err != nil || !slices.Equal(got, r.want) {
			t.Errorf("%s %s/%s desktop=%v: got %v (%v), want %v", r.in.Kind, r.goos, r.goarch, r.in.Desktop, got, err, r.want)
		}
	}
	// A pre-release keeps its dash in the file names (packaging plain_names).
	got, _ := Assets(Installation{Kind: KindLinuxDeb}, "linux", "amd64", "0.3.0-rc.1")
	if !slices.Equal(got, []string{"e5extend_0.3.0-rc.1-1_amd64.deb"}) {
		t.Errorf("pre-release deb: %v", got)
	}
	for _, bad := range []row{
		{Installation{Kind: KindDocker}, "linux", "amd64", nil},
		{Installation{Kind: KindUnknown}, "linux", "amd64", nil},
		{Installation{Kind: KindLinuxStandalone}, "linux", "riscv64", nil},
		{Installation{Kind: KindWindowsSetup}, "windows", "386", nil},
		{Installation{Kind: KindLinuxDeb}, "linux", "s390x", nil},
	} {
		if got, err := Assets(bad.in, bad.goos, bad.goarch, v); err == nil {
			t.Errorf("%s %s/%s: got %v, want an error", bad.in.Kind, bad.goos, bad.goarch, got)
		}
	}
}

func TestPlanFor(t *testing.T) {
	key := testKeyText(t)
	i := &Installer{PublicKey: key, GOOS: "linux", GOARCH: "amd64"}
	cases := []struct {
		in      Installation
		cur     string
		mode    string
		reason  string
		keyless bool
	}{
		{Installation{Kind: KindLinuxStandalone, Exe: "/x/e5extend"}, "0.2.0", ModeAuto, "", false},
		{Installation{Kind: KindDocker}, "0.2.0", ModeManual, ReasonDocker, false},
		{Installation{Kind: KindLinuxStandalone, Exe: "/x/e5extend"}, "dev", ModeManual, ReasonDev, false},
		{Installation{Kind: KindUnknown}, "0.2.0", ModeManual, ReasonUnsupported, false},
		{Installation{Kind: KindLinuxStandalone, Exe: "/x/e5extend"}, "0.2.0", ModeManual, ReasonNoKey, true},
	}
	for _, c := range cases {
		inst := *i
		if c.keyless {
			inst.PublicKey = placeholderKey
		}
		p := inst.PlanFor(c.in, c.cur)
		if p.Mode != c.mode || p.Reason != c.reason {
			t.Errorf("%s %s keyless=%v: %+v", c.in.Kind, c.cur, c.keyless, p)
		}
	}
	// An architecture without release files cannot upgrade itself.
	odd := &Installer{PublicKey: key, GOOS: "linux", GOARCH: "riscv64"}
	if p := odd.PlanFor(Installation{Kind: KindLinuxStandalone, Exe: "/x/e5extend"}, "0.2.0"); p.Mode != ModeManual || p.Reason != ReasonUnsupported {
		t.Errorf("riscv64: %+v", p)
	}
}

func testKeyText(t *testing.T) string {
	t.Helper()
	return testKey(t).Public().String()
}
