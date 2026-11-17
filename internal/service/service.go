// Package service installs, removes and queries the background job that runs
// `e5extend run`: a systemd user unit on Linux, a LaunchAgent on macOS and a
// scheduled task on Windows.
//
// The files that get written are produced by pure functions, and every
// external command goes through a Runner, so nothing here needs the real
// service manager in tests.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// Names of the registered job.
const (
	UnitName = "e5extend.service" // systemd user unit
	Label    = "io.github.kkazuhak.e5extend"
	TaskName = "e5extend" // Windows scheduled task

	// KeyFileName is the master key file written by `--key-file`.
	KeyFileName = "master.key"
)

const headlessWindowsOnly = "--headless is only for Windows"

// Platforms accepted by New.
const (
	Linux   = "linux"
	Darwin  = "darwin"
	Windows = "windows"
)

// Output is what a command printed and how it ended.
type Output struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner starts external commands. A non-zero exit is reported as an error
// together with the Output, so callers can still read what was printed.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (Output, error)
}

// ExecRunner runs real commands.
type ExecRunner struct {
	Timeout time.Duration // per command; 60 seconds when zero
}

// buildCmd prepares an external command. On Windows the command gets no
// console window, so a program built for the GUI subsystem does not flash one
// each time it asks the Task Scheduler.
func buildCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	return cmd
}

// Run implements Runner.
func (r ExecRunner) Run(ctx context.Context, name string, args ...string) (Output, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var so, se bytes.Buffer
	cmd := buildCmd(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	out := Output{Stdout: so.String(), Stderr: se.String()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			out.ExitCode = ee.ExitCode()
			return out, fmt.Errorf("%s: exit status %d%s", commandLine(name, args), out.ExitCode, tail(out.Stderr))
		}
		out.ExitCode = -1
		return out, fmt.Errorf("%s: %w", commandLine(name, args), err)
	}
	return out, nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return ": " + s
}

func commandLine(name string, args []string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

// Env describes the machine the manager works on.
type Env struct {
	GOOS string // linux, darwin or windows
	Home string // the user's home directory
	UID  string // numeric user ID (launchd's gui/<uid> domain)
	User string // account name; DOMAIN\user on Windows
	// DataDir is where the Windows installer keeps the task definition it
	// imported; Uninstall removes it from there.
	DataDir string
	// ConfigDir is where a headless install keeps its key file; Uninstall
	// removes a leftover one from there.
	ConfigDir string
	Runner    Runner
	// Elevated says whether this process runs with administrator rights
	// (Windows). Nil means unknown; it only shapes error advice.
	Elevated func() bool
	// WriteKeyFile and AuditKeyFile handle the DPAPI key file of a headless
	// Windows install; nil uses the real ones in package vault.
	WriteKeyFile func(path string, key []byte) error
	AuditKeyFile func(path string) vault.KeyFileAudit
}

// InstallOptions say what to install.
type InstallOptions struct {
	Exe       string // absolute path of the binary to run
	ConfigDir string
	DataDir   string

	// KeyFile (Linux) writes the master key to ConfigDir/master.key and hands
	// it to the unit with LoadCredential=. MasterKey supplies the key.
	KeyFile   bool
	MasterKey func() ([]byte, error)
	// EnableLinger (Linux) runs `loginctl enable-linger` when it is off.
	EnableLinger bool
	// SystemdVersion (Linux) is the major version of systemd; zero means
	// unknown, which Install fills in by asking `systemctl --version`.
	SystemdVersion int
	// Headless (Windows) makes the task run whether or not the user is signed
	// in (logon type S4U) and starts it at boot; the master key (MasterKey)
	// goes to ConfigDir/master.key.dpapi, encrypted with DPAPI at machine
	// scope and restricted to the user, SYSTEM and Administrators.
	Headless bool
	// ExplicitDirs hands ConfigDir and DataDir to the job when they come from
	// E5EXTEND_*_DIR overrides: as environment on macOS, as --config-dir and
	// --data-dir arguments on Windows (systemd units always carry them).
	ExplicitDirs bool
}

// Result lists what was done.
type Result struct {
	Actions  []string // what was done, in order
	Files    []string // files written or removed
	Warnings []string // things the user should not miss
	Manual   []string // commands the user has to run themselves
}

// Tri is a yes/no answer that may be unknown.
type Tri int

// Tri values.
const (
	Unknown Tri = iota
	No
	Yes
)

func (t Tri) String() string {
	switch t {
	case Yes:
		return "yes"
	case No:
		return "no"
	}
	return "unknown"
}

func triOf(b bool) Tri {
	if b {
		return Yes
	}
	return No
}

// Status is what the platform says about the job.
type Status struct {
	Platform  string
	Installed bool
	Enabled   Tri
	Running   Tri
	// Linger is only meaningful on Linux.
	Linger Tri
	// Headless is only meaningful on Windows: the task runs whether or not
	// the user is signed in (logon type S4U).
	Headless Tri
	// KeyFile, KeyFileMissing, KeyFileErr and KeyFileBroad describe the DPAPI
	// key file of a headless task: its path (from the task definition), that
	// it does not exist, why it cannot be read or decrypted (no path in it),
	// and the accounts beyond user, SYSTEM and Administrators its ACL admits.
	KeyFile        string
	KeyFileMissing bool
	KeyFileErr     string
	KeyFileBroad   []string
	// KeyFileACLErr: why the key file's access list could not be read.
	// KeyFileDir is its folder and KeyFileDirBroad the accounts besides the
	// user, SYSTEM and Administrators that can change that folder.
	KeyFileACLErr   string
	KeyFileDir      string
	KeyFileDirBroad []string
	Details         []string
}

// File is a file the installation writes.
type File struct {
	Path string
	Mode fs.FileMode
	Data []byte
	// Lazy, when set, produces Data at write time (a secret that must not be
	// computed just to print a plan).
	Lazy   func() ([]byte, error)
	Secret bool   // never printed
	Show   string // printed instead of Data when set (Data may be UTF-16)
	// Write, when set, stores the data instead of the plain writeFile (the
	// DPAPI key file needs its own encryption and ACL).
	Write func(path string, data []byte) error
	// Note is printed under the file in a plan.
	Note string
}

// Command is an external command of the installation.
type Command struct {
	Args        []string
	Optional    bool // a failure is not an error
	IfInstalled bool // only when the job was installed before
	// Retry runs the command once more after Wait if it failed (the old job
	// may still be exiting).
	Retry bool
	Wait  time.Duration
	// Settle is a pause after the command, for things that need a moment.
	Settle time.Duration
	Why    string
}

// Pauses used by plans; tests set them to zero.
var (
	retryWait  = 2 * time.Second
	settleWait = time.Second
)

// Plan is everything an installation does, without doing it.
type Plan struct {
	Dirs     []string
	Files    []File
	Commands []Command
	// After lists steps that depend on what the machine answers; they are
	// only described here and carried out by Install.
	After []string
}

// Format renders the plan for `service install --print`.
func (p Plan) Format() string {
	var sb strings.Builder
	for _, d := range p.Dirs {
		fmt.Fprintf(&sb, "create directory %s\n", d)
	}
	if len(p.Dirs) > 0 {
		sb.WriteString("\n")
	}
	for _, f := range p.Files {
		if f.Write != nil {
			fmt.Fprintf(&sb, "write %s\n", f.Path)
		} else {
			fmt.Fprintf(&sb, "write %s (mode %04o)\n", f.Path, f.Mode.Perm())
		}
		switch {
		case f.Secret:
			sb.WriteString("  <secret content not shown>\n")
			if f.Note != "" {
				sb.WriteString("  " + f.Note + "\n")
			}
			sb.WriteString("\n")
		default:
			body := f.Show
			if body == "" {
				body = string(f.Data)
			}
			sb.WriteString("----\n" + strings.TrimRight(body, "\n") + "\n----\n\n")
		}
	}
	if len(p.Commands) > 0 {
		sb.WriteString("run, in this order:\n")
	}
	for _, c := range p.Commands {
		line := "  " + strings.Join(c.Args, " ")
		var notes []string
		if c.IfInstalled {
			notes = append(notes, "only if already installed")
		}
		if c.Optional {
			notes = append(notes, "failure ignored")
		}
		if c.Retry {
			notes = append(notes, "tried once more if it fails")
		}
		if c.Why != "" {
			notes = append(notes, c.Why)
		}
		if len(notes) > 0 {
			line += "   # " + strings.Join(notes, "; ")
		}
		sb.WriteString(line + "\n")
	}
	for _, a := range p.After {
		sb.WriteString("then: " + a + "\n")
	}
	return sb.String()
}

// Manager installs and queries the background job of one platform.
type Manager interface {
	// Plan returns what Install would do, without doing it.
	Plan(opts InstallOptions) (Plan, error)
	// Install writes the files, registers the job, enables it and starts it.
	Install(opts InstallOptions) (Result, error)
	// Uninstall stops and unregisters the job and removes its files.
	Uninstall() (Result, error)
	// Start starts an installed job that is not running. It does not rewrite
	// any file; a job that is not installed is an error.
	Start() (Result, error)
	// Restart stops the job if it runs and starts it again, so it runs the
	// program file as it is now (after an upgrade replaced it). A job that is
	// not installed is an error.
	Restart() (Result, error)
	// Status asks the platform about the job.
	Status() (Status, error)
}

// ErrNotInstalled means Start found no job to start.
var ErrNotInstalled = errors.New("the background service is not installed")

// ErrUnsupported means the platform has no background job support.
var ErrUnsupported = errors.New("background service is not supported on this platform")

// New returns the Manager for env.GOOS.
func New(env Env) (Manager, error) {
	if env.Runner == nil {
		env.Runner = ExecRunner{}
	}
	switch env.GOOS {
	case Linux:
		return &systemd{env: env}, nil
	case Darwin:
		return &launchd{env: env}, nil
	case Windows:
		return &schtasks{env: env}, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrUnsupported, env.GOOS)
}

// ---- running a plan ----

// apply carries out the files and commands of p. installed says whether the
// job existed before (for IfInstalled commands).
func apply(ctx context.Context, r Runner, p Plan, installed bool, res *Result) error {
	for _, d := range p.Dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", d, err)
		}
	}
	for _, f := range p.Files {
		data := f.Data
		if f.Lazy != nil {
			var err error
			if data, err = f.Lazy(); err != nil {
				return err
			}
		}
		write := writeFile
		if f.Write != nil {
			write = func(path string, data []byte, _ fs.FileMode) error { return f.Write(path, data) }
		}
		if err := write(f.Path, data, f.Mode); err != nil {
			return err
		}
		res.Files = append(res.Files, f.Path)
		res.Actions = append(res.Actions, "wrote "+f.Path)
	}
	for _, c := range p.Commands {
		if c.IfInstalled && !installed {
			continue
		}
		_, err := r.Run(ctx, c.Args[0], c.Args[1:]...)
		if err != nil && c.Retry {
			time.Sleep(c.Wait)
			_, err = r.Run(ctx, c.Args[0], c.Args[1:]...)
		}
		if c.Settle > 0 {
			time.Sleep(c.Settle)
		}
		if err != nil {
			if c.Optional {
				continue
			}
			return err
		}
		res.Actions = append(res.Actions, "ran "+strings.Join(c.Args, " "))
	}
	return nil
}

// writeFile replaces path with data through a temporary file in the same
// directory. Unlike paths.WriteFileAtomic it leaves the directory's mode
// alone, because the directory may be shared (~/.config/systemd/user).
func writeFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil && !isWindows() {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func isWindows() bool { return runtime.GOOS == "windows" }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// removeFile deletes p and reports whether it existed.
func removeFile(p string) (bool, error) {
	err := os.Remove(p)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

// ---- paths ----

// join builds a path for goos regardless of the host, so printing the plan of
// another platform shows that platform's separators.
func join(goos string, parts ...string) string {
	if goos == Windows {
		var sb strings.Builder
		for i, p := range parts {
			if i > 0 {
				sb.WriteByte('\\')
				p = strings.TrimLeft(p, `\/`)
			}
			sb.WriteString(strings.TrimRight(p, `\/`))
		}
		return sb.String()
	}
	return path.Join(parts...)
}

// winBase and winDir split a Windows path on either kind of slash.
func winBase(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func winDir(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[:i]
	}
	return "."
}

// ---- the executable ----

// CheckExecutable refuses paths that will not exist later: `go run` build
// caches and temporary directories.
func CheckExecutable(path string, tempDirs ...string) error {
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, `\`, "/")) }
	p := norm(path)
	switch {
	case strings.Contains(p, "/go-build"):
		return fmt.Errorf("%s looks like a `go run` build; build a binary first (go build -o e5extend ./cmd/e5extend) and run that", path)
	}
	// macOS runs a quarantined app from a random read-only copy (App
	// Translocation) until it is moved out of Downloads or the disk image.
	if strings.Contains(p, "/apptranslocation/") ||
		(strings.HasPrefix(p, "/volumes/") && strings.Contains(p, ".app/contents/macos/")) {
		return fmt.Errorf("%s is a temporary copy (the app runs from the disk image or from a quarantined download); "+
			"move E5 Developer Extend to the Applications folder, open it from there and try again", path)
	}
	dirs := append([]string{"/tmp", "/var/tmp", "/private/tmp", "/private/var/folders", "/var/folders"}, tempDirs...)
	for _, d := range dirs {
		d = strings.TrimRight(norm(d), "/")
		if d != "" && (p == d || strings.HasPrefix(p, d+"/")) {
			return fmt.Errorf("%s is in a temporary directory that may be cleared; install the binary somewhere permanent first", path)
		}
	}
	if strings.Contains(p, "/appdata/local/temp/") || strings.Contains(p, "/windows/temp/") {
		return fmt.Errorf("%s is in a temporary directory that may be cleared; install the binary somewhere permanent first", path)
	}
	return nil
}

// BackgroundExeName is the Windows GUI-subsystem build that does not open a
// console window.
const BackgroundExeName = "e5extend-bg.exe"

// PickWindowsExe prefers e5extend-bg.exe next to exe. It returns the program to
// register and a warning when only the console build is available.
func PickWindowsExe(exe string, exists func(string) bool) (path, warning string) {
	if strings.EqualFold(winBase(exe), BackgroundExeName) {
		return exe, ""
	}
	bg := join(Windows, winDir(exe), BackgroundExeName)
	if exists(bg) {
		return bg, ""
	}
	return exe, "e5extend-bg.exe was not found next to " + winBase(exe) +
		"; the task will use the console build, which shows a console window each time it starts"
}
