package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/ops"
)

func (a *app) newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Export everything to one file, or import it on another machine",
		Long: "Move e5renew to another machine (also another operating system) with one file.\n" +
			"The file is encrypted with a backup passphrase you choose; on import the secrets are\n" +
			"encrypted again with the new machine's own master key.",
	}
	cmd.AddCommand(a.newBackupExportCmd(), a.newBackupImportCmd())
	return cmd
}

func (a *app) newBackupExportCmd() *cobra.Command {
	var opts ops.ExportOptions
	cmd := &cobra.Command{
		Use:   "export <file>",
		Short: "Write config, secrets, sign-ins and state to one encrypted file",
		Long: "Write the config, the vault contents (secrets, saved sign-ins, the webhook address), the state and\n" +
			"the interface settings to <file>, encrypted with a backup passphrase (at least 8 characters).\n" +
			"The passphrase is read without echo from a terminal (asked twice), or one line from stdin in scripts.\n" +
			"The master key is not exported. Suggested extension: .e5backup",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			// Refuse before asking for a passphrase.
			if err := ops.CheckBackupTarget(path, opts.Force); err != nil {
				return err
			}
			pass, err := a.readBackupPassphrase(cmd, true)
			if err != nil {
				return err
			}
			sum, err := a.ops().ExportBackup(contextOrBackground(cmd), path, pass, opts)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "backup written to %s\n", sum.Path)
			fmt.Fprintf(out, "accounts: %s\n", listOrNone(sum.Accounts))
			fmt.Fprintf(out, "vault entries: %d\n", sum.VaultEntries)
			if sum.IncludesLogs {
				fmt.Fprintf(out, "call records: %d file(s)\n", sum.LogFiles)
			} else {
				fmt.Fprintln(out, "call records: not included (use --with-logs)")
			}
			for _, n := range sum.Notes {
				fmt.Fprintf(out, "note: %s\n", n)
			}
			fmt.Fprintln(out, "Keep the passphrase: without it the file cannot be opened, and it cannot be recovered.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.WithLogs, "with-logs", false, "also include the call records")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "overwrite the file if it exists")
	return cmd
}

func (a *app) newBackupImportCmd() *cobra.Command {
	var merge, replace bool
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Restore a backup file on this machine",
		Long: "Restore a backup made by `backup export`. By default this machine must have no accounts and no\n" +
			"vault data. --merge adds the backup's accounts that this machine does not have (it stops, changing nothing,\n" +
			"if a name exists on both). --replace replaces this machine's accounts, schedule, alert settings,\n" +
			"webhook and state with the backup's. The [vault] setting of this machine is always kept.\n" +
			"The passphrase is read without echo from a terminal, or one line from stdin in scripts.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := ops.ImportDefault
			switch {
			case merge:
				mode = ops.ImportMerge
			case replace:
				mode = ops.ImportReplace
			}
			pass, err := a.readBackupPassphrase(cmd, false)
			if err != nil {
				return err
			}
			sum, err := a.ops().ImportBackup(contextOrBackground(cmd), args[0], pass, mode)
			if err != nil {
				return err
			}
			printImportSummary(cmd.OutOrStdout(), sum)
			return nil
		},
	}
	cmd.Flags().BoolVar(&merge, "merge", false, "add the accounts this machine does not have")
	cmd.Flags().BoolVar(&replace, "replace", false, "replace this machine's accounts and settings with the backup's")
	cmd.MarkFlagsMutuallyExclusive("merge", "replace")
	return cmd
}

func printImportSummary(out io.Writer, s ops.ImportSummary) {
	fmt.Fprintf(out, "imported (%s mode) from a backup made %s by e5renew %s on %s\n", s.Mode, s.Created, s.SourceVersion, s.SourceOS)
	fmt.Fprintf(out, "accounts: %s\n", listOrNone(s.Accounts))
	fmt.Fprintf(out, "vault entries written: %d\n", s.VaultEntries)
	if s.NewMasterKey {
		fmt.Fprintln(out, "this machine had no master key: a new one was created in the system keyring (keep it safe; see `e5renew vault init`)")
	}
	for _, sk := range s.Skipped {
		fmt.Fprintf(out, "left out: %s\n", sk)
	}
	for _, n := range s.Notes {
		fmt.Fprintf(out, "note: %s\n", n)
	}
	fmt.Fprintf(out, "next: %s\n", s.Hint)
}

func listOrNone(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// readBackupPassphrase reads the backup passphrase. On a terminal an export
// asks twice; elsewhere one line is read from stdin. It never comes from the
// environment or the command line, where it would leak into process lists and shell history.
func (a *app) readBackupPassphrase(cmd *cobra.Command, confirm bool) (string, error) {
	pass, err := readSecret(cmd, "Backup passphrase: ")
	if err != nil {
		return "", err
	}
	if confirm && a.isInteractive(cmd.InOrStdin()) {
		again, err := readSecret(cmd, "Repeat the passphrase: ")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", errors.New("the passphrases do not match")
		}
	}
	return pass, nil
}
