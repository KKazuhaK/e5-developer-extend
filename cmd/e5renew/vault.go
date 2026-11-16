package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/vault"
)

func (a *app) newVaultCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "vault", Short: "Manage the vault master key"}
	cmd.AddCommand(a.newVaultInitCmd(), a.newVaultResetCmd())
	return cmd
}

func (a *app) newVaultInitCmd() *cobra.Command {
	var printKey bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the vault master key",
		Long: "Create the vault master key and store it in the system keyring, or with --print-key\n" +
			"print a new one for E5RENEW_MASTER_KEY or a systemd credential (nothing is stored).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printKey {
				key, err := vault.GenerateKey()
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), vault.EncodeKey(key))
				fmt.Fprintf(cmd.ErrOrStderr(), "Keep this key safe: without it the vault cannot be read.\n"+
					"Use it as %s, or save it as the systemd credential %q.\n", vault.EnvMasterKey, vault.CredentialName)
				return nil
			}
			dir, err := paths.VaultDir()
			if err != nil {
				return err
			}
			err = vault.InitKeyring(dir)
			switch {
			case errors.Is(err, vault.ErrKeyExists):
				fmt.Fprintln(cmd.OutOrStdout(), "a master key already exists in the system keyring; nothing to do")
				return nil
			case err != nil:
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "master key stored in the system keyring")
			return nil
		},
	}
	cmd.Flags().BoolVar(&printKey, "print-key", false, "print a new key instead of storing it")
	return cmd
}

func (a *app) newVaultResetCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Delete all vault data (secrets and token caches)",
		Long: "Delete every encrypted file in the vault. Use this only when the master key is lost.\n" +
			"Accounts stay in the config; enter their secrets again with `account set-secret` and sign in again.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := paths.VaultDir()
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(cmd.ErrOrStderr(), "Delete all vault data in %s? This cannot be undone. [y/N] ", dir)
				ans, err := readLineCtx(contextOrBackground(cmd), cmd.InOrStdin())
				if errors.Is(err, errInterrupted) {
					return err
				}
				if err != nil {
					return errors.New("no confirmation received; pass --yes to reset without asking")
				}
				if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
					return errors.New("aborted")
				}
			}
			if err := vault.Reset(dir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "vault data deleted (%s)\n", dir)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
