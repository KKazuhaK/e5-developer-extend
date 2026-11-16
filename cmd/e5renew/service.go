package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/engine"
	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/service"
	"github.com/KKazuhaK/e5-renew/internal/vault"
)

// svcIdentity overrides who the service is installed for; tests only.
type svcIdentity struct {
	Home, UID, User string
}

// serviceEnv describes this machine for the service manager.
func (a *app) serviceEnv() (service.Env, error) {
	env := service.Env{GOOS: runtime.GOOS, Runner: a.runner}
	if env.Runner == nil {
		env.Runner = service.ExecRunner{}
	}
	env.Home, env.UID, env.User = a.svc.Home, a.svc.UID, a.svc.User
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
			env.User = firstNonEmpty(os.Getenv("USER"), os.Getenv("USERNAME"))
		}
	}
	dir, err := paths.DataDir()
	if err != nil {
		return env, err
	}
	env.DataDir = dir
	return env, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *app) serviceManager() (service.Manager, error) {
	env, err := a.serviceEnv()
	if err != nil {
		return nil, err
	}
	return service.New(env)
}

// executable returns the binary to register: symlinks resolved, temporary
// locations refused, and on Windows the console-less build when present.
func (a *app) executable(strict bool) (string, []string, error) {
	get := a.exe
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

func (a *app) newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Run e5renew in the background at login (systemd, launchd or Task Scheduler)",
	}
	cmd.AddCommand(a.newServiceInstallCmd(), a.newServiceUninstallCmd(), a.newServiceStatusCmd())
	return cmd
}

func (a *app) newServiceInstallCmd() *cobra.Command {
	var keyFile, linger, printOnly bool
	var platform string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and start the background job for the current user",
		Long: "Install e5renew as a background job that starts at login and restarts after failures:\n" +
			"  Linux    a systemd user unit (~/.config/systemd/user/e5renew.service)\n" +
			"  macOS    a LaunchAgent (~/Library/LaunchAgents)\n" +
			"  Windows  a scheduled task that runs at logon\n\n" +
			"With --print nothing is written or run; the files and commands are shown instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if platform != "" && !printOnly {
				return errors.New("--platform only works together with --print")
			}
			if printOnly {
				return a.printServicePlan(cmd, platform, keyFile, linger)
			}
			return a.installService(cmd, keyFile, linger)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&keyFile, "key-file", false, "Linux: store the master key in ConfigDir/master.key and hand it to the unit (for servers without a keyring)")
	f.BoolVar(&linger, "enable-linger", false, "Linux: run loginctl enable-linger so the service keeps running after you log out")
	f.BoolVar(&printOnly, "print", false, "show the files and commands without writing or running anything")
	f.StringVar(&platform, "platform", "", "linux, darwin or windows (only with --print)")
	_ = f.MarkHidden("platform")
	return cmd
}

func (a *app) installOptions(cfgBackend string, keyFile, linger, strict bool) (service.InstallOptions, []string, error) {
	exe, warns, err := a.executable(strict)
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
	o := service.InstallOptions{
		Exe: exe, ConfigDir: cfgDir, DataDir: dataDir, KeyFile: keyFile, EnableLinger: linger,
		ExplicitDirs: os.Getenv("E5RENEW_CONFIG_DIR") != "" || os.Getenv("E5RENEW_DATA_DIR") != "",
	}
	if keyFile {
		if cfgBackend == vault.BackendKeyring {
			return o, nil, errors.New("vault.backend is \"keyring\", which never reads the key file; set backend = \"auto\" or \"file\" in the config first")
		}
		vdir, err := paths.VaultDir()
		if err != nil {
			return o, nil, err
		}
		o.MasterKey = func() ([]byte, error) {
			key, _, err := vault.ResolveMasterKey(cfgBackend, vdir)
			return key, err
		}
	}
	return o, warns, nil
}

func (a *app) installService(cmd *cobra.Command, keyFile, linger bool) error {
	cfg, _, err := a.loadConfigOrDefault()
	if err != nil {
		return err
	}
	opts, warns, err := a.installOptions(cfg.Vault.Backend, keyFile, linger, true)
	if err != nil {
		return err
	}
	m, err := a.serviceManager()
	if err != nil {
		return err
	}
	res, err := m.Install(opts)
	out := cmd.OutOrStdout()
	if err != nil {
		printResult(out, res)
		return err
	}
	res.Warnings = append(warns, res.Warnings...)
	if hint := keyHint(runtime.GOOS, keyFile, os.Getenv); hint != "" {
		res.Warnings = append(res.Warnings, hint)
	}
	fmt.Fprintln(out, "background job installed and started")
	printResult(out, res)
	fmt.Fprintln(out, "next: e5renew service status   (and: e5renew doctor)")
	return nil
}

// keyHint explains why the service may not find the master key.
func keyHint(goos string, keyFile bool, getenv func(string) string) string {
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

func printResult(out io.Writer, res service.Result) {
	for _, a := range res.Actions {
		fmt.Fprintln(out, "  "+a)
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(out, "WARNING: "+w)
	}
	if len(res.Manual) > 0 {
		fmt.Fprintln(out, "run this yourself:")
		for _, c := range res.Manual {
			fmt.Fprintln(out, "  "+c)
		}
	}
}

// printServicePlan shows what install would do. For another platform it uses
// made-up user names and paths, since the real ones would be wrong.
func (a *app) printServicePlan(cmd *cobra.Command, platform string, keyFile, linger bool) error {
	out := cmd.OutOrStdout()
	if platform == "" {
		platform = runtime.GOOS
	}
	var env service.Env
	var o service.InstallOptions
	switch {
	case platform == runtime.GOOS:
		cfg, _, err := a.loadConfigOrDefault()
		if err != nil {
			return err
		}
		if env, err = a.serviceEnv(); err != nil {
			return err
		}
		var warns []string
		if o, warns, err = a.installOptions(cfg.Vault.Backend, keyFile, linger, false); err != nil {
			return err
		}
		for _, w := range warns {
			fmt.Fprintln(out, "WARNING: "+w)
		}
	case platform == service.Linux:
		env = service.Env{GOOS: platform, Home: "/home/you", User: "you"}
		o = service.InstallOptions{Exe: "/home/you/.local/bin/e5renew", ConfigDir: "/home/you/.config/e5renew",
			DataDir: "/home/you/.local/share/e5renew", KeyFile: keyFile, EnableLinger: linger,
			MasterKey: func() ([]byte, error) { return nil, nil }}
	case platform == service.Darwin:
		env = service.Env{GOOS: platform, Home: "/Users/you", UID: "501", User: "you"}
		dir := "/Users/you/Library/Application Support/e5renew"
		o = service.InstallOptions{Exe: "/usr/local/bin/e5renew", ConfigDir: dir, DataDir: dir, KeyFile: keyFile}
	case platform == service.Windows:
		env = service.Env{GOOS: platform, Home: `C:\Users\you`, User: `DESKTOP-PC\you`, DataDir: `C:\Users\you\AppData\Local\e5renew`}
		exe, _ := service.PickWindowsExe(`C:\Users\you\bin\e5renew.exe`, func(string) bool { return true })
		o = service.InstallOptions{Exe: exe, ConfigDir: `C:\Users\you\AppData\Roaming\e5renew`,
			DataDir: `C:\Users\you\AppData\Local\e5renew`, KeyFile: keyFile}
	default:
		return fmt.Errorf("--platform must be linux, darwin or windows, got %q", platform)
	}
	env.GOOS = platform
	// Printing must never start a command.
	env.Runner = &service.FakeRunner{}
	m, err := service.New(env)
	if err != nil {
		return err
	}
	plan, err := m.Plan(o)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "platform: %s (nothing is written or run)\n\n", platform)
	fmt.Fprint(out, plan.Format())
	return nil
}

func (a *app) newServiceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the background job",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := a.serviceManager()
			if err != nil {
				return err
			}
			res, err := m.Uninstall()
			printResult(cmd.OutOrStdout(), res)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "done; your configuration, vault and logs were not touched")
			return nil
		},
	}
}

func (a *app) newServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the background job is installed, enabled and running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			m, err := a.serviceManager()
			if err != nil {
				return err
			}
			st, err := m.Status()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "platform:  %s\n", st.Platform)
			fmt.Fprintf(out, "installed: %s\n", yesNo(st.Installed))
			if st.Installed {
				fmt.Fprintf(out, "enabled:   %s\n", st.Enabled)
				fmt.Fprintf(out, "running:   %s\n", st.Running)
			}
			if st.Platform == service.Linux && st.Installed {
				fmt.Fprintf(out, "linger:    %s\n", st.Linger)
			}
			for _, d := range st.Details {
				fmt.Fprintf(out, "           %s\n", d)
			}
			dataDir, err := paths.DataDir()
			if err != nil {
				return err
			}
			if engine.Running(dataDir) {
				line := "engine:    running"
				if hb, err := engine.ReadHeartbeat(dataDir); err == nil {
					line += fmt.Sprintf(" (last heartbeat %s ago)", time.Since(hb).Round(time.Second))
				}
				fmt.Fprintln(out, line)
			} else {
				fmt.Fprintln(out, "engine:    not running")
			}
			if !st.Installed {
				fmt.Fprintln(out, "install it with: e5renew service install")
			} else if st.Platform == service.Linux && st.Linger == service.No {
				fmt.Fprintln(out, "WARNING: lingering is off, so the service stops when you log out; run: loginctl enable-linger "+firstNonEmpty(os.Getenv("USER"), "$USER"))
			}
			return nil
		},
	}
}
