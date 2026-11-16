package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/paths"
)

const configTemplate = `# e5renew configuration. Secrets are never stored here; they live in the vault.
version = 1

[schedule]
interval_min = "40m"        # lower bound of the random pause between rounds
interval_max = "2h"         # upper bound
apis_per_round_min = 3
apis_per_round_max = 8
random_order = true
work_days = ["mon", "tue", "wed", "thu", "fri"]   # empty means every day
work_start = "08:00"        # local time; start == end means all day
work_end = "22:00"
# period_start = 2026-12-01 # optional run period
# period_end = 2027-12-01

[network]
proxy = ""                  # empty uses HTTPS_PROXY and friends

[notify]
webhook_url = ""

[vault]
backend = "auto"            # auto | keyring | file

# Add accounts with: e5renew account add <name> --auth ... --tenant ... --client-id ...
`

func (a *app) newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect and create the configuration file"}
	cmd.AddCommand(a.newConfigPathCmd(), a.newConfigInitCmd(), a.newConfigValidateCmd())
	return cmd
}

func (a *app) newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the configuration and data locations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, err := paths.ConfigFile()
			if err != nil {
				return err
			}
			data, err := paths.DataDir()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "config file: %s\n", file)
			fmt.Fprintf(out, "data dir:    %s\n", data)
			return nil
		},
	}
}

func (a *app) newConfigInitCmd() *cobra.Command {
	var printTemplate bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a default config.toml (an existing file is left alone)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printTemplate {
				fmt.Fprint(cmd.OutOrStdout(), configTemplate)
				return nil
			}
			path, err := paths.ConfigFile()
			if err != nil {
				return err
			}
			unlock, err := a.lockConfig(cmd)
			if err != nil {
				return err
			}
			defer unlock()
			if _, err := os.Stat(path); err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s already exists; left unchanged\n", path)
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if _, err := config.Parse([]byte(configTemplate)); err != nil {
				return fmt.Errorf("built-in template is invalid: %w", err)
			}
			if err := paths.WriteFileAtomic(path, []byte(configTemplate)); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&printTemplate, "print-template", false, "print the commented template instead of creating a file")
	return cmd
}

func (a *app) newConfigValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check the configuration and list every problem",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := paths.ConfigFile()
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if errors.Is(err, config.ErrNotFound) {
				return fmt.Errorf("%w\nrun: e5renew config init", err)
			}
			if err != nil {
				if cfg == nil {
					return err
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "%s has problems:\n", path)
				for _, e := range splitErrors(err) {
					fmt.Fprintf(out, "  - %s\n", e)
				}
				return errors.New("configuration is invalid")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s is valid (%d accounts)\n", path, len(cfg.Accounts))
			return nil
		},
	}
}

func splitErrors(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}
