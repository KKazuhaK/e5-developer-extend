package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/update"
)

func (a *app) newUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for a newer version and upgrade to it",
		Long: "e5extend asks GitHub once a day (in the background engine) whether a newer release exists; nothing is ever\n" +
			"installed without being asked. 'update check' looks now, 'update install' upgrades this installation:\n" +
			"the download is verified with the release signature and its SHA-256 before anything is replaced.\n" +
			"Turn the daily check off with [update] check = false in config.toml.",
	}
	cmd.AddCommand(a.newUpdateCheckCmd(), a.newUpdateInstallCmd())
	return cmd
}

func (a *app) newUpdateCheckCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Look for a newer version now",
		Long: "Ask GitHub for the newest release of the configured channel ([update] channel) and say whether it is newer,\n" +
			"whether this installation can upgrade itself and, if not, why. The exit code is 0 when the check worked,\n" +
			"whether or not an update exists.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, err := a.ops().UpdateCheck(contextOrBackground(cmd), true)
			out := cmd.OutOrStdout()
			if asJSON {
				if jerr := writeUpdateJSON(out, info); jerr != nil {
					return jerr
				}
			} else {
				printUpdateInfo(out, info)
			}
			if err != nil {
				return fmt.Errorf("the check failed: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

func writeUpdateJSON(w io.Writer, info ops.UpdateInfo) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(info)
}

// planText says in a few words whether this installation can upgrade itself.
func planText(info ops.UpdateInfo) string {
	if info.Mode == update.ModeAuto {
		return "can upgrade itself (" + info.Kind + ")"
	}
	switch info.Reason {
	case update.ReasonDocker:
		return "a Docker image: pull the new image instead"
	case update.ReasonDev:
		return "a development build: it is not upgraded automatically"
	case update.ReasonNoKey:
		return "this build has no release signing key, so it cannot verify a download; use the release page"
	}
	return "cannot upgrade itself (" + info.Kind + "); use the release page"
}

func printUpdateInfo(out io.Writer, info ops.UpdateInfo) {
	fmt.Fprintf(out, "this version: %s (channel %s)\n", info.Current, info.Channel)
	switch {
	case info.Dev:
		fmt.Fprintln(out, "development build: no update is offered")
	case info.Latest == "":
		fmt.Fprintln(out, "latest: unknown (no release found)")
	default:
		fmt.Fprintf(out, "latest: %s\n", info.Latest)
	}
	if info.Available {
		fmt.Fprintf(out, "a newer version is available: %s\n", info.URL)
		fmt.Fprintf(out, "this installation: %s\n", planText(info))
		if info.Mode == update.ModeAuto {
			fmt.Fprintln(out, "upgrade with: e5extend update install")
		}
	} else if !info.Dev && info.Latest != "" {
		fmt.Fprintln(out, "you are up to date")
	}
	if !info.CheckEnabled {
		fmt.Fprintln(out, "(the daily check is off: [update] check = false)")
	}
}

func (a *app) newUpdateInstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Upgrade to the newest version",
		Long: "Download the newest release, verify its signature and checksum, and install it over this installation the way it\n" +
			"was installed (installer, archive, package or app). A Docker image cannot upgrade itself. Linux packages need\n" +
			"administrator rights: when you are not root the exact sudo command is printed instead. The background\n" +
			"service is restarted afterwards.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := contextOrBackground(cmd)
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			o := a.ops()
			info, err := o.UpdateCheck(ctx, true)
			if err != nil {
				return fmt.Errorf("the check failed: %w", err)
			}
			switch {
			case info.Dev:
				return errors.New("this is a development build; it is not upgraded automatically")
			case !info.Available:
				fmt.Fprintf(out, "you are up to date (%s)\n", info.Current)
				return nil
			case info.Mode != update.ModeAuto:
				fmt.Fprintf(out, "version %s is available: %s\n", info.Latest, info.URL)
				return fmt.Errorf("%s", planText(info))
			}
			fmt.Fprintf(out, "upgrade %s -> %s (%s)\n", info.Current, info.Latest, info.Kind)
			if !yes {
				if !a.isInteractive(cmd.InOrStdin()) {
					return errors.New("no terminal to confirm on; pass --yes to upgrade without asking")
				}
				fmt.Fprint(errOut, "Download and install it now? [y/N] ")
				ans, err := readLineCtx(ctx, cmd.InOrStdin())
				if errors.Is(err, errInterrupted) {
					return err
				}
				if v := strings.ToLower(strings.TrimSpace(ans)); err != nil || (v != "y" && v != "yes") {
					return errors.New("aborted")
				}
			}
			last := ""
			res, err := o.UpdateInstall(ctx, ops.UpdateInstallOptions{Progress: func(p update.Progress) {
				if p.Stage != last {
					last = p.Stage
					fmt.Fprintf(errOut, "%s...\n", stageText(p.Stage))
				}
			}})
			printUpdateResult(out, res)
			var me *update.ManualError
			if errors.As(err, &me) {
				return fmt.Errorf("%s", planText(ops.UpdateInfo{Mode: me.Plan.Mode, Reason: me.Plan.Reason, Kind: string(me.Plan.Kind)}))
			}
			return err
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func stageText(stage string) string {
	switch stage {
	case "download":
		return "downloading"
	case "verify":
		return "verifying the signature and checksum"
	case "install":
		return "installing"
	}
	return stage
}

func printUpdateResult(out io.Writer, res ops.UpdateResult) {
	if res.UpToDate {
		fmt.Fprintln(out, "nothing newer to install")
		return
	}
	for _, s := range res.Steps {
		fmt.Fprintf(out, "- %s\n", s.Text)
	}
	switch res.Service {
	case "restarted":
		fmt.Fprintln(out, "- restarted the background service")
	case "failed":
		fmt.Fprintf(out, "- the background service was not restarted: %s\n", res.ServiceNote)
	}
	if len(res.Manual) > 0 {
		fmt.Fprintln(out, "this needs you: run")
		for _, m := range res.Manual {
			fmt.Fprintf(out, "    %s\n", m)
		}
		if res.KeepDir != "" {
			fmt.Fprintf(out, "(the verified download is in %s)\n", res.KeepDir)
		}
		return
	}
	switch {
	case res.ExitNow:
		fmt.Fprintf(out, "the installer is running; version %s will be in place in a few seconds\n", res.Version)
	case res.Done:
		fmt.Fprintf(out, "upgraded to %s\n", res.Version)
		if res.RestartApp {
			fmt.Fprintln(out, "restart the tray app and any open window to use it")
		}
	}
}
