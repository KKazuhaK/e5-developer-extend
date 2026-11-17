package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Plan modes.
const (
	ModeAuto   = "auto"   // the program can upgrade itself
	ModeManual = "manual" // it can only point to the release page
)

// Reasons a plan is manual (stable codes; the interfaces translate them).
const (
	ReasonDocker      = "docker"
	ReasonDev         = "dev"
	ReasonNoKey       = "no_key"
	ReasonUnsupported = "unsupported"
)

// Plan says whether and how an installation can upgrade itself. It looks at
// the installation only, never at the network or the disk.
type Plan struct {
	Kind   Kind   `json:"kind"`
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
}

// Step is one thing an upgrade did. Code is stable; Text is English.
type Step struct {
	Code string `json:"code"`
	Arg  string `json:"arg,omitempty"`
	Text string `json:"text"`
}

// Outcome is what Install did.
type Outcome struct {
	Version string `json:"version"`
	Steps   []Step `json:"steps"`
	// Manual lists commands the person has to run themselves because the
	// program could not (no rights, a folder it may not write to).
	Manual []string `json:"manual"`
	// Done is true when the new version is in place (or, with ExitNow, is being put in place).
	Done bool `json:"done"`
	// ExitNow means a setup program was started that replaces this program's
	// files: the caller must exit at once.
	ExitNow bool `json:"exitNow"`
	// RestartService means the background service still runs the old program.
	RestartService bool `json:"restartService"`
	// RestartApp means the tray and the window still run the old program.
	RestartApp bool `json:"restartApp"`
	// KeepDir is the folder with the downloaded files that Manual refers to.
	KeepDir string `json:"keepDir,omitempty"`
}

func (o *Outcome) step(code, arg, text string) { o.Steps = append(o.Steps, Step{code, arg, text}) }

// Progress reports a download or a stage of the upgrade.
type Progress struct {
	// Stage is "download", "verify", "install".
	Stage string `json:"stage"`
	File  string `json:"file,omitempty"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

// Installer upgrades the installation described by an Installation. Every
// outside effect goes through a field, so tests run it on temporary folders.
type Installer struct {
	// PublicKey is PublicKeyText.
	PublicKey    string
	GOOS, GOARCH string
	Downloader   *Downloader
	// TempRoot holds the working folder; os.TempDir() when empty.
	TempRoot string
	// Run runs a command and returns its combined output.
	Run func(ctx context.Context, name string, args ...string) (string, error)
	// StartDetached starts a program that outlives this process.
	StartDetached func(name string, args ...string) error
	// LookPath finds a program on PATH (exec.LookPath).
	LookPath func(string) (string, error)
	// Geteuid returns the user ID, -1 where there is none.
	Geteuid func() int
	// UsePkexec lets a package upgrade ask for rights with pkexec when the
	// program is not root; the command line leaves that to the person (sudo).
	UsePkexec bool
	Progress  func(Progress)
}

func (i *Installer) progress(p Progress) {
	if i.Progress != nil {
		i.Progress(p)
	}
}

// PlanFor says what can be done for an installation at the running version.
func (i *Installer) PlanFor(in Installation, current string) Plan {
	p := Plan{Kind: in.Kind, Mode: ModeManual}
	switch {
	case in.Kind == KindDocker:
		p.Reason = ReasonDocker
	case IsDevBuild(current):
		p.Reason = ReasonDev
	case in.Kind == KindUnknown:
		p.Reason = ReasonUnsupported
	case (in.Kind == KindLinuxStandalone || in.Kind == KindMacCLI) && pathBase(in.Exe) != "e5extend":
		// The archive holds "e5extend"; putting it over another file (the
		// desktop app, a renamed copy) would replace the wrong program.
		p.Reason = ReasonUnsupported
	case !HasPublicKey(i.PublicKey):
		p.Reason = ReasonNoKey
	default:
		if _, err := Assets(in, i.GOOS, i.GOARCH, "0.0.0"); err != nil {
			p.Reason = ReasonUnsupported
			return p
		}
		p.Mode = ModeAuto
	}
	return p
}

// Install downloads, verifies and installs release rel over the installation in.
// It refuses anything that is not newer than current.
func (i *Installer) Install(ctx context.Context, rel Release, in Installation, current string) (Outcome, error) {
	var out Outcome
	v, err := rel.Version()
	if err != nil {
		return out, err
	}
	out.Version = v.String()
	if !Newer(current, out.Version) {
		return out, fmt.Errorf("version %s is not newer than the running %s", out.Version, current)
	}
	if p := i.PlanFor(in, current); p.Mode != ModeAuto {
		return out, &ManualError{Plan: p}
	}
	names, err := Assets(in, i.GOOS, i.GOARCH, out.Version)
	if err != nil {
		return out, err
	}
	// Refuse before downloading anything when the release cannot be verified.
	if _, ok := rel.Asset(SigName); !ok {
		return out, ErrNoSignature
	}
	want := append(append([]string(nil), names...), SumsName, SigName)
	var assets []Asset
	for _, n := range want {
		a, ok := rel.Asset(n)
		if !ok {
			return out, fmt.Errorf("release %s has no file %s", rel.Tag, n)
		}
		assets = append(assets, a)
	}

	root := i.TempRoot
	if root == "" {
		root = os.TempDir()
	}
	cleanOldWorkDirs(root)
	dir, err := os.MkdirTemp(root, workPrefix)
	if err != nil {
		return out, fmt.Errorf("make a working folder: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()

	d := i.Downloader
	if d == nil {
		d = &Downloader{}
	}
	for _, a := range assets {
		name := a.Name
		if _, err := d.Fetch(ctx, a, dir, func(done, total int64) {
			i.progress(Progress{Stage: "download", File: name, Done: done, Total: total})
		}); err != nil {
			return out, err
		}
	}
	out.step("downloaded", out.Version, "downloaded "+strings.Join(names, ", "))
	i.progress(Progress{Stage: "verify"})
	if err := VerifyBundle(i.PublicKey, dir, names); err != nil {
		return out, err
	}
	out.step("verified", "", "verified the signature and the checksums")

	i.progress(Progress{Stage: "install"})
	switch in.Kind {
	case KindWindowsSetup:
		err = i.windowsSetup(dir, names[0], &out)
		keep = err == nil // the setup program runs from this folder
	case KindWindowsCLI:
		err = i.replaceArchive(dir, names[0], in, true, &out)
	case KindLinuxStandalone, KindMacCLI:
		err = i.replaceArchive(dir, names[0], in, false, &out)
	case KindMacApp:
		err = i.macApp(ctx, dir, names[0], in, &out)
	case KindLinuxDeb, KindLinuxRPM:
		err = i.linuxPackages(ctx, dir, names, in, &out)
	default:
		err = &ManualError{Plan: Plan{Kind: in.Kind, Mode: ModeManual, Reason: ReasonUnsupported}}
	}
	if len(out.Manual) > 0 {
		keep = true
		out.KeepDir = dir
	}
	return out, err
}

// ManualError means this installation cannot be upgraded by the program.
type ManualError struct{ Plan Plan }

func (e *ManualError) Error() string {
	switch e.Plan.Reason {
	case ReasonDocker:
		return "this is a Docker image; pull the new image instead"
	case ReasonDev:
		return "this is a development build; it is not upgraded automatically"
	case ReasonNoKey:
		return ErrNoPublicKey.Error()
	}
	return "this installation cannot be upgraded by the program; download the new version from the release page"
}

const workPrefix = "e5extend-update-"

// cleanOldWorkDirs removes working folders of earlier upgrades that are over a day old.
func cleanOldWorkDirs(root string) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), workPrefix) {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// windowsSetup starts the NSIS installer. It stops the tray and the
// background task, replaces the files, starts the task again and, when the tray
// was running, the tray too (packaging/windows/installer.nsi), so the caller
// only has to get out of the way.
func (i *Installer) windowsSetup(dir, name string, out *Outcome) error {
	if i.StartDetached == nil {
		return errors.New("cannot start the installer here")
	}
	if err := i.StartDetached(filepath.Join(dir, name), "/S"); err != nil {
		return fmt.Errorf("start the installer: %w", err)
	}
	out.step("installer_started", name, "started the installer; this program is closing so it can be replaced")
	out.Done, out.ExitNow = true, true
	return nil
}

// replaceArchive swaps the programs of a command-line archive in.
func (i *Installer) replaceArchive(dir, archive string, in Installation, aside bool, out *Outcome) error {
	targets := []string{filepath.Base(in.Exe)}
	if aside {
		// e5extend.exe and the console-less e5extend-bg.exe the scheduled task runs.
		targets = nil
		for _, n := range []string{"e5extend.exe", "e5extend-bg.exe"} {
			if _, err := os.Stat(filepath.Join(in.Dir, n)); err == nil {
				targets = append(targets, n)
			}
		}
		if len(targets) == 0 {
			targets = []string{filepath.Base(in.Exe)}
		}
	}
	srcDir := filepath.Join(dir, "unpacked")
	if err := os.Mkdir(srcDir, 0o700); err != nil {
		return err
	}
	var err error
	extracted := map[string]string{}
	if strings.HasSuffix(archive, ".zip") {
		err = extractZip(filepath.Join(dir, archive), targets, srcDir)
		for _, n := range targets {
			extracted[n] = filepath.Join(srcDir, n)
		}
	} else {
		// The archive holds the program as "e5extend".
		src := filepath.Join(srcDir, "e5extend")
		err = extractTarGz(filepath.Join(dir, archive), "e5extend", src)
		extracted[targets[0]] = src
	}
	if err != nil {
		return err
	}
	if !dirWritable(in.Dir) {
		cmd := fmt.Sprintf("sudo install -m 0755 %s %s", shellQuote(extracted[targets[0]]), shellQuote(in.Exe))
		if aside {
			cmd = fmt.Sprintf("copy the files in %s over those in %s", srcDir, in.Dir)
		}
		out.Manual = append(out.Manual, cmd)
		out.step("manual", in.Dir, in.Dir+" is not writable for this user")
		return nil
	}
	var swapped []string
	for _, n := range targets {
		dst := filepath.Join(in.Dir, n)
		if err := replaceFile(dst, extracted[n], aside); err != nil {
			// All or nothing: a mix of old and new programs is the worst result.
			if aside {
				for _, d := range swapped {
					undoAside(d)
				}
			}
			return err
		}
		swapped = append(swapped, dst)
		out.step("replaced", n, "replaced "+n)
	}
	out.Done, out.RestartService = true, true
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// macApp replaces the .app bundle with the one in the disk image.
func (i *Installer) macApp(ctx context.Context, dir, dmg string, in Installation, out *Outcome) error {
	if i.Run == nil {
		return errors.New("cannot run hdiutil here")
	}
	parent := filepath.Dir(in.AppPath)
	if !dirWritable(parent) {
		out.Manual = append(out.Manual, "open "+shellQuote(filepath.Join(dir, dmg))+" and drag E5 Developer Extend into "+parent+", then replace the old app")
		out.step("manual", parent, parent+" is not writable for this user")
		return nil
	}
	mnt := filepath.Join(dir, "mnt")
	if err := os.Mkdir(mnt, 0o700); err != nil {
		return err
	}
	if o, err := i.Run(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mnt, filepath.Join(dir, dmg)); err != nil {
		return fmt.Errorf("open the disk image: %w\n%s", err, tail(o))
	}
	defer func() { _, _ = i.Run(context.WithoutCancel(ctx), "hdiutil", "detach", mnt, "-force") }()
	src := filepath.Join(mnt, filepath.Base(in.AppPath))
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("the disk image does not contain %s", filepath.Base(in.AppPath))
	}
	stage := filepath.Join(parent, ".e5extend-update-"+filepath.Base(in.AppPath))
	_ = os.RemoveAll(stage)
	if o, err := i.Run(ctx, "ditto", src, stage); err != nil {
		_ = os.RemoveAll(stage)
		return fmt.Errorf("copy the app: %w\n%s", err, tail(o))
	}
	old := in.AppPath + asideSuffix
	_ = os.RemoveAll(old)
	if err := os.Rename(in.AppPath, old); err != nil {
		_ = os.RemoveAll(stage)
		return fmt.Errorf("move the old app aside: %w", err)
	}
	if err := os.Rename(stage, in.AppPath); err != nil {
		if rerr := os.Rename(old, in.AppPath); rerr != nil {
			return fmt.Errorf("put the new app in place: %w (and the old one could not be put back: %v)", err, rerr)
		}
		_ = os.RemoveAll(stage)
		return fmt.Errorf("put the new app in place: %w", err)
	}
	_ = os.RemoveAll(old)
	out.step("replaced", filepath.Base(in.AppPath), "replaced "+in.AppPath)
	out.Done, out.RestartService, out.RestartApp = true, true, true
	return nil
}

// linuxPackages installs the deb or rpm files (the command-line package and,
// when the desktop app is installed, the desktop package) in one transaction.
func (i *Installer) linuxPackages(ctx context.Context, dir string, names []string, in Installation, out *Outcome) error {
	var files []string
	for _, n := range names {
		files = append(files, filepath.Join(dir, n))
	}
	var cmd []string
	if in.Kind == KindLinuxDeb {
		cmd = append([]string{"apt-get", "install", "-y"}, files...)
	} else {
		tool := ""
		for _, t := range []string{"dnf", "yum"} {
			if i.LookPath != nil {
				if _, err := i.LookPath(t); err == nil {
					tool = t
					break
				}
			}
		}
		if tool == "" {
			tool = "dnf"
		}
		cmd = append([]string{tool, "install", "-y"}, files...)
	}
	manual := "sudo " + joinQuoted(cmd)
	if in.Kind == KindLinuxDeb {
		manual = "sudo env DEBIAN_FRONTEND=noninteractive " + joinQuoted(cmd)
	}
	euid := -1
	if i.Geteuid != nil {
		euid = i.Geteuid()
	}
	var full []string
	switch {
	case euid == 0:
		full = cmd
	case i.UsePkexec && i.lookPath("pkexec"):
		full = append([]string{"pkexec"}, cmd...)
	default:
		out.Manual = append(out.Manual, manual)
		out.step("manual", "", "installing a package needs administrator rights")
		return nil
	}
	if in.Kind == KindLinuxDeb {
		// pkexec drops the environment; apt-get must not ask questions.
		full = insertEnv(full)
	}
	if i.Run == nil {
		return errors.New("cannot run the package manager here")
	}
	o, err := i.Run(ctx, full[0], full[1:]...)
	if err != nil {
		if i.UsePkexec && len(full) > 0 && full[0] == "pkexec" && strings.Contains(err.Error(), "126") {
			// pkexec exit 126: the person dismissed the password prompt.
			return errors.New("the authentication was cancelled; nothing was installed")
		}
		return fmt.Errorf("%s failed: %w\n%s", cmd[0], err, tail(o))
	}
	out.step("package_installed", strings.Join(names, ", "), "installed "+strings.Join(names, ", "))
	out.Done, out.RestartService, out.RestartApp = true, true, in.Desktop
	return nil
}

func (i *Installer) lookPath(name string) bool {
	if i.LookPath == nil {
		return false
	}
	_, err := i.LookPath(name)
	return err == nil
}

// insertEnv puts "env DEBIAN_FRONTEND=noninteractive" in front of the apt-get command.
func insertEnv(cmd []string) []string {
	for k, a := range cmd {
		if a == "apt-get" {
			out := append([]string(nil), cmd[:k]...)
			out = append(out, "env", "DEBIAN_FRONTEND=noninteractive")
			return append(out, cmd[k:]...)
		}
	}
	return cmd
}

func joinQuoted(args []string) string {
	q := make([]string, len(args))
	for k, a := range args {
		if strings.ContainsAny(a, " \t'\"$`\\") {
			q[k] = shellQuote(a)
		} else {
			q[k] = a
		}
	}
	return strings.Join(q, " ")
}

// tail is the last lines of a command's output, for an error message.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 1500 {
		s = "..." + s[len(s)-1500:]
	}
	return s
}
