package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/vault"
)

func (a *app) newAccountCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "account", Short: "Manage Microsoft 365 accounts"}
	cmd.AddCommand(a.newAccountAddCmd(), a.newAccountListCmd(), a.newAccountLoginCmd(),
		a.newAccountTestCmd(), a.newAccountRemoveCmd(), a.newAccountSetSecretCmd(),
		a.newAccountPauseCmd(), a.newAccountResumeCmd())
	return cmd
}

func (a *app) newAccountAddCmd() *cobra.Command {
	var acct config.Account
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add an account to the configuration",
		Long: "Add an account. For client_secret and password the secret is read without echo\n" +
			"from the terminal, or one line from stdin when it is not a terminal, and stored in the vault.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			acct.Name = args[0]
			// Validate the account on its own first, so a bad flag fails before any prompt.
			probe := config.Default()
			probe.Accounts = []config.Account{acct}
			if err := probe.Validate(); err != nil {
				// The probe config holds only this account; drop its list index from the messages.
				return fmt.Errorf("invalid account:\n%s", strings.ReplaceAll(err.Error(), "accounts[0].", ""))
			}

			var secret string
			if acct.NeedsSecret() {
				var err error
				if secret, err = promptNewSecret(cmd, acct); err != nil {
					return err
				}
			}

			unlock, err := a.lockConfig(cmd)
			if err != nil {
				return err
			}
			defer unlock()
			cfg, path, err := a.loadConfigOrDefault()
			if err != nil {
				return err
			}
			if _, exists := cfg.Find(acct.Name); exists {
				return fmt.Errorf("account %q already exists", acct.Name)
			}
			cfg.Accounts = append(cfg.Accounts, acct)
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid account:\n%w", err)
			}

			var v *vault.Vault
			if acct.NeedsSecret() {
				if v, err = a.openVault(cfg); err != nil {
					return err
				}
				if err := v.Put(vault.SecretKey(acct.Name), []byte(secret)); err != nil {
					return withHint(fmt.Errorf("store secret: %w", err), acct.Name)
				}
			}
			if err := cfg.Save(path); err != nil {
				if v != nil {
					_ = v.Delete(vault.SecretKey(acct.Name))
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added account %q (%s)\n", acct.Name, acct.Auth)
			fmt.Fprintf(cmd.OutOrStdout(), "next: e5renew account login %s\n", acct.Name)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&acct.Auth, "auth", "", "device_code, browser, client_secret or password")
	f.StringVar(&acct.Tenant, "tenant", "", "tenant ID or domain (organizations for delegated multi-tenant)")
	f.StringVar(&acct.ClientID, "client-id", "", "application (client) ID of your app registration")
	f.StringVar(&acct.Username, "username", "", "sign-in name (password) or target user (client_secret)")
	f.StringVar(&acct.SecretExpires, "secret-expires", "", "client secret expiry date, YYYY-MM-DD")
	for _, name := range []string{"auth", "tenant", "client-id"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// promptNewSecret asks for the client secret or password of acct.
func promptNewSecret(cmd *cobra.Command, acct config.Account) (string, error) {
	what := "Client secret"
	if acct.Auth == config.AuthPassword {
		what = "Password"
	}
	secret, err := readSecret(cmd, fmt.Sprintf("%s for account %s: ", what, acct.Name))
	if err != nil {
		return "", err
	}
	if secret == "" {
		return "", errors.New("the secret must not be empty")
	}
	return secret, nil
}

func (a *app) newAccountSetSecretCmd() *cobra.Command {
	var expires string
	cmd := &cobra.Command{
		Use:   "set-secret <name>",
		Short: "Replace the stored client secret or password of an account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			acct, err := a.findAccount(cfg, name)
			if err != nil {
				return err
			}
			if !acct.NeedsSecret() {
				return fmt.Errorf("account %q uses %s and has no stored secret", name, acct.Auth)
			}
			if expires != "" {
				if _, err := time.Parse("2006-01-02", expires); err != nil {
					return fmt.Errorf("--secret-expires: not a YYYY-MM-DD date: %q", expires)
				}
			}
			secret, err := promptNewSecret(cmd, *acct)
			if err != nil {
				return err
			}
			v, err := a.openVault(cfg)
			if err != nil {
				return err
			}
			if err := v.Put(vault.SecretKey(name), []byte(secret)); err != nil {
				return withHint(fmt.Errorf("store secret: %w", err), name)
			}
			if expires != "" {
				err := a.updateAccount(cmd, name, func(ac *config.Account) { ac.SecretExpires = expires })
				if err != nil {
					return err
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stored the new secret for account %q\nnext: e5renew account login %s   (verifies it)\n", name, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&expires, "secret-expires", "", "new expiry date, YYYY-MM-DD")
	return cmd
}

// updateAccount reloads the config under the lock, applies change to the named
// account and saves. It is a no-op if the account has disappeared.
func (a *app) updateAccount(cmd *cobra.Command, name string, change func(*config.Account)) error {
	unlock, err := a.lockConfig(cmd)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, path, err := a.loadConfig()
	if err != nil {
		return err
	}
	acct, ok := cfg.Find(name)
	if !ok {
		return nil
	}
	change(acct)
	return cfg.Save(path)
}

func (a *app) newAccountListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List accounts and their login state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := a.loadConfigOrDefault()
			if err != nil {
				return err
			}
			if len(cfg.Accounts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no accounts; add one with: e5renew account add")
				return nil
			}
			v, err := a.openVault(cfg)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tAUTH\tTENANT\tSTATUS")
			for _, acct := range cfg.Accounts {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", acct.Name, acct.Auth, acct.Tenant, accountStatus(v, acct))
			}
			return tw.Flush()
		},
	}
}

// accountStatus summarizes local state only; it never touches the network or the master key.
func accountStatus(v *vault.Vault, acct config.Account) string {
	var parts []string
	has := func(key string) bool { ok, _ := v.Has(key); return ok }

	switch acct.Auth {
	case config.AuthClientSecret:
		if has(vault.SecretKey(acct.Name)) {
			parts = append(parts, "secret stored")
		} else {
			parts = append(parts, "secret missing")
		}
	default:
		if acct.Auth == config.AuthPassword && !has(vault.SecretKey(acct.Name)) {
			parts = append(parts, "password missing")
		} else if acct.HomeAccountID != "" && has(vault.TokenCacheKey(acct.Name)) {
			parts = append(parts, "logged in")
		} else {
			parts = append(parts, "not logged in")
		}
	}
	if acct.SecretExpires != "" {
		if t, err := time.Parse("2006-01-02", acct.SecretExpires); err == nil {
			if time.Now().After(t.AddDate(0, 0, 1)) {
				parts = append(parts, "secret EXPIRED "+acct.SecretExpires)
			} else {
				parts = append(parts, "secret expires "+acct.SecretExpires)
			}
		}
	}
	if acct.Paused {
		parts = append(parts, "paused")
	}
	return strings.Join(parts, ", ")
}

type cliUI struct{ cmd *cobra.Command }

func (u cliUI) DeviceCode(message string) { fmt.Fprintln(u.cmd.ErrOrStderr(), message) }

func (u cliUI) OpenBrowser(url string) error {
	fmt.Fprintf(u.cmd.ErrOrStderr(), "Opening your browser to sign in. If it does not open, visit:\n%s\n", url)
	// The URL is already printed, so a failed launch is not fatal.
	_ = browser.OpenURL(url)
	return nil
}

func (a *app) newAccountLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login <name>",
		Short: "Sign in (device code or browser) or verify the stored secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			acct, err := a.findAccount(cfg, name)
			if err != nil {
				return err
			}
			au, err := a.authenticator(cfg, *acct)
			if err != nil {
				return withHint(err, name)
			}
			tok, err := au.Login(contextOrBackground(cmd), cliUI{cmd})
			if err != nil {
				return withHint(err, name)
			}
			if acct.Delegated() && tok.HomeAccountID != "" && tok.HomeAccountID != acct.HomeAccountID {
				err := a.updateAccount(cmd, name, func(ac *config.Account) { ac.HomeAccountID = tok.HomeAccountID })
				if err != nil {
					return fmt.Errorf("signed in, but saving the config failed: %w", err)
				}
			}
			out := cmd.OutOrStdout()
			if acct.Delegated() {
				fmt.Fprintf(out, "signed in as %s\n", tok.Account)
			} else {
				fmt.Fprintf(out, "client secret verified for application %s\n", tok.Account)
			}
			printTokenInfo(cmd, tok)
			return nil
		},
	}
}

func printTokenInfo(cmd *cobra.Command, tok auth.Token) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "token expires: %s\n", tok.ExpiresOn.Local().Format(time.RFC3339))
	fmt.Fprintf(out, "granted permissions (%d): %s\n", len(tok.Scopes), strings.Join(tok.Scopes, " "))
}

func (a *app) newAccountTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <name>",
		Short: "Get a token silently and show who it is for and what it grants",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			acct, err := a.findAccount(cfg, name)
			if err != nil {
				return err
			}
			au, err := a.authenticator(cfg, *acct)
			if err != nil {
				return withHint(err, name)
			}
			ctx, cancel := context.WithTimeout(contextOrBackground(cmd), time.Minute)
			defer cancel()
			tok, err := au.Token(ctx)
			if err != nil {
				return withHint(err, name)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "account: %s (%s)\n", tok.Account, name)
			printTokenInfo(cmd, tok)
			return nil
		},
	}
}

func (a *app) newAccountRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an account, its stored secret and its token cache",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			if _, err := a.findAccount(cfg, name); err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(cmd.ErrOrStderr(), "Remove account %q and delete its stored secret and token cache? [y/N] ", name)
				ans, err := readLineCtx(contextOrBackground(cmd), cmd.InOrStdin())
				if errors.Is(err, errInterrupted) {
					return err
				}
				if err != nil {
					return errors.New("no confirmation received; pass --yes to remove without asking")
				}
				if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
					return errors.New("aborted")
				}
			}

			unlock, err := a.lockConfig(cmd)
			if err != nil {
				return err
			}
			defer unlock()
			if cfg, _, err = a.loadConfig(); err != nil {
				return err
			}
			path, err := paths.ConfigFile()
			if err != nil {
				return err
			}
			if !cfg.Remove(name) {
				return fmt.Errorf("no account named %q", name)
			}
			if err := cfg.Save(path); err != nil {
				return err
			}
			v, err := a.openVault(cfg)
			if err != nil {
				return err
			}
			for _, key := range []string{vault.SecretKey(name), vault.TokenCacheKey(name)} {
				if err := v.Delete(key); err != nil && !errors.Is(err, vault.ErrNotFound) {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed account %q\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
