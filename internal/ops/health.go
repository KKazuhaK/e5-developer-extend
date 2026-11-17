package ops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/doctor"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// Doctor runs the health checks. offline skips the ones that need the network.
func (o *Ops) Doctor(ctx context.Context, offline bool) ([]doctor.Check, error) {
	dataDir, err := paths.DataDir()
	if err != nil {
		return nil, err
	}
	cfgPath, err := paths.ConfigFile()
	if err != nil {
		return nil, err
	}
	d := doctor.Deps{
		GOOS: runtime.GOOS, DataDir: dataDir, GraphBase: o.GraphBase,
		LoadConfig: func() (*config.Config, error) { return config.Load(cfgPath) },
		OpenVault: func(cfg *config.Config) (doctor.Vault, error) {
			return o.OpenVault(cfg)
		},
		NewAuth: func(cfg *config.Config, acct config.Account) (auth.Authenticator, error) {
			return o.Authenticator(cfg, acct)
		},
		HTTP: func(cfg *config.Config) *http.Client {
			proxy := ""
			if cfg != nil {
				proxy = cfg.Network.Proxy
			}
			return auth.NewHTTPClient(proxy)
		},
		Hint: Hint, HintCode: HintCode,
	}
	if m, err := o.ServiceManager(); err == nil {
		d.Service = m.Status
	} else if !errors.Is(err, service.ErrUnsupported) {
		d.Service = func() (service.Status, error) { return service.Status{}, err }
	}
	return doctor.Run(ctx, d, doctor.Options{Offline: offline}), nil
}

// ServiceEnv describes this machine for the service manager.
func (o *Ops) ServiceEnv() (service.Env, error) {
	env := service.Env{GOOS: FirstNonEmpty(o.Platform, runtime.GOOS), Runner: o.Runner}
	if env.Runner == nil {
		env.Runner = service.ExecRunner{}
	}
	env.Home, env.UID, env.User = o.Svc.Home, o.Svc.UID, o.Svc.User
	if env.Home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return env, fmt.Errorf("find the home directory: %w", err)
		}
		env.Home = home
	}
	if env.UID == "" {
		env.UID = strconv.Itoa(os.Getuid())
	}
	if env.User == "" {
		if u, err := user.Current(); err == nil && u.Username != "" {
			env.User = u.Username
		} else {
			env.User = FirstNonEmpty(os.Getenv("USER"), os.Getenv("USERNAME"))
		}
	}
	dir, err := paths.DataDir()
	if err != nil {
		return env, err
	}
	env.DataDir = dir
	return env, nil
}

// FirstNonEmpty returns the first argument that is not "".
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ServiceManager returns the manager of the background job for this platform.
func (o *Ops) ServiceManager() (service.Manager, error) {
	env, err := o.ServiceEnv()
	if err != nil {
		return nil, err
	}
	return service.New(env)
}

// executable returns the binary to register: symlinks resolved, temporary
// locations refused, and on Windows the console-less build when present.
func (o *Ops) executable(strict bool) (string, []string, error) {
	get := o.Exe
	if get == nil {
		get = os.Executable
	}
	exe, err := get()
	if err != nil {
		return "", nil, fmt.Errorf("find this program's path: %w", err)
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return "", nil, err
	}
	var warnings []string
	if err := service.CheckExecutable(exe, os.TempDir()); err != nil {
		if strict {
			return "", nil, err
		}
		warnings = append(warnings, "install would refuse this binary: "+err.Error())
	}
	if runtime.GOOS == service.Windows {
		var w string
		exe, w = service.PickWindowsExe(exe, func(p string) bool { _, err := os.Stat(p); return err == nil })
		if w != "" {
			warnings = append(warnings, w)
		}
	}
	return exe, warnings, nil
}

func (o *Ops) installOptions(cfgBackend string, keyFile, linger, strict bool) (service.InstallOptions, []string, error) {
	exe, warns, err := o.executable(strict)
	if err != nil {
		return service.InstallOptions{}, nil, err
	}
	cfgDir, err := paths.ConfigDir()
	if err != nil {
		return service.InstallOptions{}, nil, err
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return service.InstallOptions{}, nil, err
	}
	opts := service.InstallOptions{
		Exe: exe, ConfigDir: cfgDir, DataDir: dataDir, KeyFile: keyFile, EnableLinger: linger,
		ExplicitDirs: os.Getenv("E5EXTEND_CONFIG_DIR") != "" || os.Getenv("E5EXTEND_DATA_DIR") != "",
	}
	if keyFile {
		if cfgBackend == vault.BackendKeyring {
			return opts, nil, errors.New("vault.backend is \"keyring\", which never reads the key file; set backend = \"auto\" or \"file\" in the config first")
		}
		vdir, err := paths.VaultDir()
		if err != nil {
			return opts, nil, err
		}
		opts.MasterKey = func() ([]byte, error) {
			key, _, err := vault.ResolveMasterKey(cfgBackend, vdir)
			return key, err
		}
	}
	return opts, warns, nil
}

// ServiceInstall installs and starts the background job for the current user.
// The result is filled in even when err is not nil, so a caller can show what was done.
func (o *Ops) ServiceInstall(keyFile, linger bool) (service.Result, error) {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return service.Result{}, err
	}
	opts, warns, err := o.installOptions(cfg.Vault.Backend, keyFile, linger, true)
	if err != nil {
		return service.Result{}, err
	}
	m, err := o.ServiceManager()
	if err != nil {
		return service.Result{}, err
	}
	res, err := m.Install(opts)
	if err != nil {
		return res, err
	}
	res.Warnings = append(warns, res.Warnings...)
	if hint := KeyHint(runtime.GOOS, keyFile, os.Getenv); hint != "" {
		res.Warnings = append(res.Warnings, hint)
	}
	return res, nil
}

// CheckServiceInstall runs the checks that need no side effects before an
// install (the binary to register, the key file options), so a caller can
// fail before it disturbs anything.
func (o *Ops) CheckServiceInstall() error {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return err
	}
	if _, _, err := o.installOptions(cfg.Vault.Backend, false, false, true); err != nil {
		return err
	}
	_, err = o.ServiceManager()
	return err
}

// EngineStoppedHint says what to do when the engine is not running, depending
// on whether a background service is installed.
func (o *Ops) EngineStoppedHint() string {
	if st, err := o.ServiceStatus(); err == nil && st.Installed {
		return "the background service is installed but its engine is not running; check: e5extend service status (and e5extend doctor)"
	}
	return "start it with: e5extend service install (background) or e5extend run (foreground)"
}

// KeyHint explains why the service may not find the master key.
func KeyHint(goos string, keyFile bool, getenv func(string) string) string {
	if goos != service.Linux || keyFile {
		return ""
	}
	switch {
	case getenv(vault.EnvMasterKey) != "":
		return vault.EnvMasterKey + " is set in this shell, but the service does not inherit it. " +
			"Install with --key-file so the service gets the key from ConfigDir/master.key"
	case getenv("CREDENTIALS_DIRECTORY") != "":
		return ""
	case getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "":
		return "this looks like a server without a desktop session: the master key is probably in the system keyring, " +
			"which a systemd user service usually cannot reach. Install with --key-file to give the service a key file " +
			"(trade-off: the key is then protected only by file permissions, mode 0600)"
	}
	return ""
}

// ServiceUninstall stops and removes the background job.
func (o *Ops) ServiceUninstall() (service.Result, error) {
	m, err := o.ServiceManager()
	if err != nil {
		return service.Result{}, err
	}
	return m.Uninstall()
}

// ServiceStart starts the installed background service without rewriting it.
func (o *Ops) ServiceStart() (service.Result, error) {
	m, err := o.ServiceManager()
	if err != nil {
		return service.Result{}, err
	}
	return m.Start()
}

// ServiceStatus asks the platform about the background job.
func (o *Ops) ServiceStatus() (service.Status, error) {
	m, err := o.ServiceManager()
	if err != nil {
		return service.Status{}, err
	}
	return m.Status()
}

// ServicePlan returns what installing the job would do, without doing it.
// For another platform it uses made-up user names and paths, since the real
// ones would be wrong. warnings are about the real binary and only come back
// for the current platform.
func (o *Ops) ServicePlan(platform string, keyFile, linger bool) (plan service.Plan, warnings []string, err error) {
	if platform == "" {
		platform = runtime.GOOS
	}
	var env service.Env
	var opts service.InstallOptions
	switch {
	case platform == runtime.GOOS:
		cfg, _, err := o.LoadConfigOrDefault()
		if err != nil {
			return service.Plan{}, nil, err
		}
		if env, err = o.ServiceEnv(); err != nil {
			return service.Plan{}, nil, err
		}
		if opts, warnings, err = o.installOptions(cfg.Vault.Backend, keyFile, linger, false); err != nil {
			return service.Plan{}, nil, err
		}
	case platform == service.Linux:
		env = service.Env{GOOS: platform, Home: "/home/you", User: "you"}
		opts = service.InstallOptions{Exe: "/home/you/.local/bin/e5extend", ConfigDir: "/home/you/.config/e5extend",
			DataDir: "/home/you/.local/share/e5extend", KeyFile: keyFile, EnableLinger: linger,
			MasterKey: func() ([]byte, error) { return nil, nil }}
	case platform == service.Darwin:
		env = service.Env{GOOS: platform, Home: "/Users/you", UID: "501", User: "you"}
		dir := "/Users/you/Library/Application Support/e5extend"
		opts = service.InstallOptions{Exe: "/usr/local/bin/e5extend", ConfigDir: dir, DataDir: dir, KeyFile: keyFile}
	case platform == service.Windows:
		env = service.Env{GOOS: platform, Home: `C:\Users\you`, User: `DESKTOP-PC\you`, DataDir: `C:\Users\you\AppData\Local\e5extend`}
		exe, _ := service.PickWindowsExe(`C:\Users\you\bin\e5extend.exe`, func(string) bool { return true })
		opts = service.InstallOptions{Exe: exe, ConfigDir: `C:\Users\you\AppData\Roaming\e5extend`,
			DataDir: `C:\Users\you\AppData\Local\e5extend`, KeyFile: keyFile}
	default:
		return service.Plan{}, nil, fmt.Errorf("--platform must be linux, darwin or windows, got %q", platform)
	}
	env.GOOS = platform
	// Planning must never start a command.
	env.Runner = &service.FakeRunner{}
	m, err := service.New(env)
	if err != nil {
		return service.Plan{}, nil, err
	}
	plan, err = m.Plan(opts)
	return plan, warnings, err
}
