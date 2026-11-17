package main

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/recommend"
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
	var auto, storedSecret bool
	var sf setupFlags
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add an account to the configuration",
		Long: "Add an account. For client_secret and password the secret is read without echo\n" +
			"from the terminal, or one line from stdin when it is not a terminal, and stored in the vault.\n\n" +
			"With --auto the app registration is created in your tenant first (see: e5extend app setup --help),\n" +
			"then the account is added and signed in. --tenant is then optional and --client-id must be left out.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			acct.Name = args[0]
			if auto {
				if storedSecret {
					return errors.New("--auto and --stored-secret exclude each other")
				}
				return a.accountAddAuto(cmd, acct, sf)
			}
			for _, name := range []string{"tenant", "client-id"} {
				if !cmd.Flags().Changed(name) {
					return fmt.Errorf("required flag %q not set (or use --auto to create the app registration)", name)
				}
			}
			// Validate the account on its own first, so a bad flag fails before any prompt.
			if err := ops.ValidateAccount(acct); err != nil {
				return err
			}

			var secret string
			if acct.NeedsSecret() && !storedSecret {
				var err error
				if secret, err = promptNewSecret(cmd, acct); err != nil {
					return err
				}
			}
			in := ops.InputOf(acct)
			in.Secret = secret
			in.UseStoredSecret = storedSecret
			in.Categories = acct.Categories
			if err := a.ops().AddAccount(contextOrBackground(cmd), in); err != nil {
				return err
			}
			a.ops().ReloadEngine()
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
	f.BoolVar(&auto, "auto", false, "create the app registration automatically (administrator sign-in), then add and sign in the account")
	f.BoolVar(&storedSecret, "stored-secret", false, "use the client secret already in the vault under this name (see: e5extend app setup --app-only)")
	sf.register(cmd)
	f.StringVar(&sf.method, "setup-auth", "", "with --auto: administrator sign-in for the setup, device_code (default for client_secret) or browser")
	_ = cmd.MarkFlagRequired("auth")
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
			o.ReloadEngine()
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
			a.ops().ReloadEngine()
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
	var recommendFlag, apply, yes, force bool
	cmd := &cobra.Command{
		Use:   "test <name>",
		Short: "Get a token silently and show who it is for and what it grants",
		Long: "Get a token silently and show who it is for and what it grants.\n\n" +
			"With --recommend it also probes, with a few read-only requests, which API categories the account can\n" +
			"really use (an account without a mailbox gets 404 from mail, calendar, contacts and To Do) and prints\n" +
			"a table plus the suggested categories. With --apply it saves the recommended categories to the account.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if (apply || yes) && !recommendFlag {
				return errors.New("--apply and --yes only work together with --recommend")
			}
			if (yes || force) && !apply {
				return errors.New("--yes and --force only work together with --apply")
			}
			tok, err := a.ops().TestAccount(contextOrBackground(cmd), name)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "account: %s (%s)\n", tok.Account, name)
			printTokenInfo(cmd, tok)
			if !recommendFlag {
				return nil
			}
			return a.accountRecommend(cmd, name, apply, yes, force)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&recommendFlag, "recommend", false, "probe which API categories this account can use and suggest the categories setting")
	f.BoolVar(&apply, "apply", false, "with --recommend: save the recommended categories to the account")
	f.BoolVar(&yes, "yes", false, "with --apply: do not ask for confirmation")
	f.BoolVar(&force, "force", false, "with --apply: save even though some probes failed temporarily")
	return cmd
}

// reasonText turns a recommendation reason code into words.
func reasonText(code string) string {
	switch head, rest, _ := strings.Cut(code, ":"); head {
	case "":
		return ""
	case recommend.ReasonMissingPermission:
		return "missing permission " + strings.ReplaceAll(rest, ",", " or ")
	case recommend.ReasonNoMailbox:
		return "no Exchange mailbox (mail, calendar, contacts and To Do answer 404)"
	case recommend.ReasonNoOneDrive:
		return "no OneDrive for this account"
	case recommend.ReasonNoTeams:
		return "no Teams license"
	case recommend.ReasonNoSharePoint:
		return "no SharePoint site"
	case recommend.ReasonNeedsAdminRole:
		return "needs an administrator role (not probed)"
	case recommend.ReasonNotSupported:
		return "not available for this sign-in type"
	case recommend.ReasonArbitraryUser:
		return "checked with an arbitrary user; set username for a reliable answer"
	case recommend.ReasonProbeFailed:
		return "could not be checked (try again later)"
	}
	return code
}

func (a *app) accountRecommend(cmd *cobra.Command, name string, apply, yes, force bool) error {
	out := cmd.OutOrStdout()
	ctx := contextOrBackground(cmd)
	rec, err := a.ops().RecommendCategories(ctx, name)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CATEGORY\tSTATUS\tREASON")
	for _, c := range rec.Categories {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Category, c.Status, reasonText(c.Reason))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	cur, err := a.ops().GetAccount(name)
	if err != nil {
		return err
	}
	// Recommended categories, plus the uncertain ones the account has now: a
	// category is never dropped just because it could not be checked.
	cats := rec.Suggested(cur.Categories)
	quoted := make([]string, len(cats))
	for i, c := range cats {
		quoted[i] = strconv.Quote(c)
	}
	fmt.Fprintf(out, "\nsuggested: categories = [%s]\n", strings.Join(quoted, ", "))
	var maybe []string
	for _, c := range rec.Categories {
		if c.Status == recommend.Maybe {
			maybe = append(maybe, c.Category)
		}
	}
	if len(maybe) > 0 {
		fmt.Fprintf(out, "uncertain (kept only if the account has them already): %s\n", strings.Join(maybe, ", "))
	}
	if !apply {
		fmt.Fprintf(out, "to save it: e5extend account test %s --recommend --apply\n", name)
		return nil
	}

	if rec.HasFailedProbes() && !force {
		return errors.New("some probes failed temporarily; try again later (nothing saved; --force saves anyway, keeping the categories that could not be checked)")
	}
	if len(cats) == 0 {
		return errors.New("no category is recommended; nothing saved (check the granted permissions above)")
	}
	if slices.Equal(sortedCopy(cur.Categories), sortedCopy(cats)) {
		fmt.Fprintln(out, "the account already uses these categories; nothing to change")
		return nil
	}
	was := "(default selection)"
	if len(cur.Categories) > 0 {
		was = strings.Join(cur.Categories, ", ")
	}
	fmt.Fprintf(out, "categories of %s: %s -> %s\n", name, was, strings.Join(cats, ", "))
	if !yes {
		fmt.Fprintf(cmd.ErrOrStderr(), "Save these categories? [y/N] ")
		ans, err := readLineCtx(ctx, cmd.InOrStdin())
		if errors.Is(err, errInterrupted) {
			return err
		}
		if err != nil {
			return errors.New("no confirmation received; pass --yes to save without asking")
		}
		if v := strings.ToLower(strings.TrimSpace(ans)); v != "y" && v != "yes" {
			return errors.New("aborted")
		}
	}
	if err := a.ops().SetCategories(ctx, name, cats); err != nil {
		return err
	}
	a.ops().ReloadEngine()
	fmt.Fprintf(out, "saved the categories of account %q\n", name)
	return nil
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
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
			o.ReloadEngine()
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
