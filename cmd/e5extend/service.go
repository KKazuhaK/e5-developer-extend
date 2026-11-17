package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
)

func (a *app) serviceManager() (service.Manager, error) { return a.ops().ServiceManager() }

func firstNonEmpty(vals ...string) string { return ops.FirstNonEmpty(vals...) }

func (a *app) newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Run e5extend in the background at login (systemd, launchd or Task Scheduler)",
	}
	cmd.AddCommand(a.newServiceInstallCmd(), a.newServiceUninstallCmd(), a.newServiceStatusCmd())
	return cmd
}

func (a *app) newServiceInstallCmd() *cobra.Command {
	var keyFile, linger, headless, printOnly bool
	var platform string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and start the background job for the current user",
		Long: "Install e5extend as a background job that starts at login and restarts after failures:\n" +
			"  Linux    a systemd user unit (~/.config/systemd/user/e5extend.service)\n" +
			"  macOS    a LaunchAgent (~/Library/LaunchAgents)\n" +
			"  Windows  a scheduled task that runs at logon (with --headless: also at boot, with nobody signed in)\n\n" +
			"With --print nothing is written or run; the files and commands are shown instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if platform != "" && !printOnly {
				return errors.New("--platform only works together with --print")
			}
			f := ops.InstallFlags{KeyFile: keyFile, Linger: linger, Headless: headless}
			if printOnly {
				return a.printServicePlan(cmd, platform, f)
			}
			return a.installService(cmd, f)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&keyFile, "key-file", false, "Linux: store the master key in ConfigDir/master.key and hand it to the unit (for servers without a keyring)")
	f.BoolVar(&linger, "enable-linger", false, "Linux: run loginctl enable-linger so the service keeps running after you log out")
	f.BoolVar(&headless, "headless", false, "Windows: run whether or not anyone is signed in and start at boot (needs an elevated prompt; the master key goes to a DPAPI-protected file)")
	f.BoolVar(&printOnly, "print", false, "show the files and commands without writing or running anything")
	f.StringVar(&platform, "platform", "", "linux, darwin or windows (only with --print)")
	_ = f.MarkHidden("platform")
	return cmd
}

func (a *app) installService(cmd *cobra.Command, flags ops.InstallFlags) error {
	res, err := a.ops().ServiceInstallWith(flags)
	out := cmd.OutOrStdout()
	if err != nil {
		printResult(out, res)
		return err
	}
	fmt.Fprintln(out, "background job installed and started")
	printResult(out, res)
	fmt.Fprintln(out, "next: e5extend service status   (and: e5extend doctor)")
	return nil
}

// keyHint explains why the service may not find the master key.
func keyHint(goos string, keyFile bool, getenv func(string) string) string {
	return ops.KeyHint(goos, keyFile, getenv)
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
func (a *app) printServicePlan(cmd *cobra.Command, platform string, flags ops.InstallFlags) error {
	out := cmd.OutOrStdout()
	if platform == "" {
		platform = runtime.GOOS
	}
	plan, warns, err := a.ops().ServicePlanWith(platform, flags)
	if err != nil {
		return err
	}
	for _, w := range warns {
		fmt.Fprintln(out, "WARNING: "+w)
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
			res, err := a.ops().ServiceUninstall()
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
			st, err := a.ops().ServiceStatus()
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
			if st.Platform == service.Windows && st.Installed {
				fmt.Fprintf(out, "headless:  %s\n", st.Headless)
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
				fmt.Fprintln(out, "install it with: e5extend service install")
			} else if st.Platform == service.Windows && st.Headless == service.Yes && (st.KeyFileMissing || st.KeyFileErr != "") {
				fmt.Fprintln(out, "WARNING: the headless task cannot read its master key file, so the engine cannot start; run from an elevated prompt: e5extend service install --headless")
			} else if st.Platform == service.Windows && st.Headless == service.Yes && len(st.KeyFileBroad) > 0 {
				fmt.Fprintln(out, "WARNING: the key file's access list admits more than you, SYSTEM and Administrators; run from an elevated prompt: e5extend service install --headless")
			} else if st.Platform == service.Linux && st.Linger == service.No {
				fmt.Fprintln(out, "WARNING: lingering is off, so the service stops when you log out; run: loginctl enable-linger "+firstNonEmpty(os.Getenv("USER"), "$USER"))
			}
			return nil
		},
	}
}
