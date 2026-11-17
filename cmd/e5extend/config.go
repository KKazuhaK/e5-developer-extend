package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
)

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
			file, data, err := a.ops().ConfigPaths()
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
				fmt.Fprint(cmd.OutOrStdout(), ops.ConfigTemplate)
				return nil
			}
			path, created, err := a.ops().ConfigInit(contextOrBackground(cmd))
			if err != nil {
				return err
			}
			if !created {
				fmt.Fprintf(cmd.OutOrStdout(), "%s already exists; left unchanged\n", path)
				return nil
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
			rep, err := a.ops().ValidateConfig()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(rep.Problems) > 0 {
				fmt.Fprintf(out, "%s has problems:\n", rep.Path)
				for _, e := range rep.Problems {
					fmt.Fprintf(out, "  - %s\n", e)
				}
				return errors.New("configuration is invalid")
			}
			fmt.Fprintf(out, "%s is valid (%d accounts)\n", rep.Path, rep.Accounts)
			return nil
		},
	}
}
