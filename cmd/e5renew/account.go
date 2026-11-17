package main

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/ops"
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
			if err := ops.ValidateAccount(acct); err != nil {
				return err
			}

			var secret string
			if acct.NeedsSecret() {
				var err error
				if secret, err = promptNewSecret(cmd, acct); err != nil {
					return err
				}
			}
			in := ops.InputOf(acct)
			in.Secret = secret
			in.Categories = acct.Categories
			if err := a.ops().AddAccount(contextOrBackground(cmd), in); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added account %q (%s)\n", acct.Name, acct.Auth)
			fmt.Fprintf(cmd.OutOrStdout(), "next: e5extend account login %s\n", acct.Name)
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
			o := a.ops()
			acct, err := o.PrepareSetSecret(name, expires)
			if err != nil {
				return err
			}
			secret, err := promptNewSecret(cmd, acct)
			if err != nil {
				return err
			}
			if err := o.SetSecret(contextOrBackground(cmd), name, secret, expires); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stored the new secret for account %q\nnext: e5extend account login %s   (verifies it)\n", name, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&expires, "secret-expires", "", "new expiry date, YYYY-MM-DD")
	return cmd
}

func (a *app) newAccountListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List accounts and their login state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := a.ops().ListAccounts()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no accounts; add one with: e5extend account add")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tAUTH\tTENANT\tSTATUS")
			for _, e := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Account.Name, e.Account.Auth, e.Account.Tenant, e.Status)
			}
			return tw.Flush()
		},
	}
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
			res, err := a.ops().Login(contextOrBackground(cmd), args[0], cliUI{cmd})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if res.Delegated {
				fmt.Fprintf(out, "signed in as %s\n", res.Account)
			} else {
				fmt.Fprintf(out, "client secret verified for application %s\n", res.Account)
			}
			printTokenInfo(cmd, res.TokenInfo)
			return nil
		},
	}
}

func printTokenInfo(cmd *cobra.Command, tok ops.TokenInfo) {
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
			tok, err := a.ops().TestAccount(contextOrBackground(cmd), name)
			if err != nil {
				return err
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
			o := a.ops()
			if err := o.CheckAccount(name); err != nil {
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
			warnings, err := o.RemoveAccount(contextOrBackground(cmd), name)
			if err != nil {
				return err
			}
			for _, w := range warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed account %q\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
