package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/appsetup"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

func (a *app) newAppCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "app", Short: "Create or update the Entra app registration"}
	cmd.AddCommand(a.newAppSetupCmd())
	return cmd
}

// setupFlags are the flags shared by `app setup` and `account add --auto`.
type setupFlags struct {
	method   string
	tenant   string
	yes      bool
	adopt    string
	newApp   bool
	noRename bool
}

func (f *setupFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.BoolVarP(&f.yes, "yes", "y", false, "do not ask for confirmation")
	fl.StringVar(&f.adopt, "adopt", "", "adopt this existing app registration (its client ID) instead of creating a new one")
	fl.BoolVar(&f.newApp, "new", false, "create a new app registration even if similar ones exist")
	fl.BoolVar(&f.noRename, "no-rename", false, "when adopting, keep the app's current name")
}

func (a *app) newAppSetupCmd() *cobra.Command {
	var f setupFlags
	var appOnly bool
	var account string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Create or update the app registration automatically",
		Long: "Sign in once as an administrator of your tenant (through Microsoft's own \"Microsoft Graph Command\n" +
			"Line Tools\" app), then create or update the app registration, add the read-only permissions,\n" +
			"and grant administrator consent. Running it again only adds what is missing.\n\n" +
			"With --app-only it sets up the client-secret identity instead and stores a new client secret in the\n" +
			"vault under --account NAME; the secret is never shown. Add the account afterwards with\n" +
			"  e5extend account add NAME --auth client_secret --tenant ... --client-id ... --stored-secret\n\n" +
			"The administrator's token is kept in memory only. Manual setup: " + appsetup.Manual,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if appOnly && account == "" {
				return errors.New("--app-only needs --account NAME: the vault entry that receives the client secret")
			}
			if !appOnly && account != "" {
				return errors.New("--account only applies with --app-only")
			}
			sess, res, err := a.runAppSetup(cmd, ops.AppSetupOptions{AppOnly: appOnly, Method: f.method, Tenant: f.tenant, Account: account}, f)
			if err != nil {
				return err
			}
			sess.Close()
			out := cmd.OutOrStdout()
			if appOnly {
				fmt.Fprintf(out, "\nnext: e5extend account add %s --auth client_secret --tenant %s --client-id %s --stored-secret --username <user>\n",
					account, res.TenantID, res.ClientID)
			} else {
				fmt.Fprintf(out, "\nnext: e5extend account add <name> --auth device_code --tenant %s --client-id %s\n", res.TenantID, res.ClientID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&appOnly, "app-only", false, "set up the client-secret identity (application permissions) instead of the delegated one")
	cmd.Flags().StringVar(&account, "account", "", "with --app-only: account name whose vault entry receives the client secret")
	cmd.Flags().StringVar(&f.method, "auth", "", "administrator sign-in: device_code (default) or browser")
	cmd.Flags().StringVar(&f.tenant, "tenant", "", "tenant ID or domain of the administrator (default: organizations)")
	f.register(cmd)
	return cmd
}

// runAppSetup runs the whole interactive flow: sign in, show the plan, ask,
// apply, print what happened. The returned session is still open (the caller
// closes it).
func (a *app) runAppSetup(cmd *cobra.Command, opts ops.AppSetupOptions, f setupFlags) (*ops.AppSetupSession, *appsetup.Result, error) {
	ctx := contextOrBackground(cmd)
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if !f.yes && !a.isInteractive(cmd.InOrStdin()) {
		return nil, nil, errors.New("this needs a terminal to confirm the plan; pass --yes to run without asking")
	}
	progress := func(m string) { fmt.Fprintln(errOut, "  "+m) }

	fmt.Fprintln(errOut, "Sign in as an administrator of your tenant. The sign-in page will say \"Microsoft Graph Command Line Tools\":")
	fmt.Fprintln(errOut, "that is Microsoft's official tool, used only this once to create the app registration.")
	sess, err := a.ops().BeginAppSetup(ctx, opts, cliUI{cmd}, progress)
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(out, "signed in as %s (tenant ID %s)\n", sess.Admin(), sess.TenantID())

	plan, err := sess.Plan(ctx)
	if err != nil {
		return nil, nil, err
	}
	printPlan(cmd, plan)

	choice, err := a.chooseExisting(cmd, plan, f)
	if err != nil {
		return nil, nil, err
	}
	// Nothing to change (a repeat run): there is nothing to confirm, and Apply
	// only reports the IDs.
	if !f.yes && len(plan.Actions) > 0 {
		fmt.Fprint(errOut, "Go ahead? [y/N] ")
		ans, err := readLineCtx(ctx, cmd.InOrStdin())
		if errors.Is(err, errInterrupted) {
			return nil, nil, err
		}
		if a := strings.ToLower(strings.TrimSpace(ans)); err != nil || (a != "y" && a != "yes") {
			return nil, nil, errors.New("aborted; nothing was changed")
		}
	}
	res, err := sess.Apply(ctx, choice)
	if err != nil {
		return nil, nil, err
	}
	printSetupResult(cmd, res)
	return sess, res, nil
}

func printPlan(cmd *cobra.Command, p *appsetup.Plan) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\nIdentity: %s\n", map[appsetup.Kind]string{appsetup.KindDelegated: "delegated (signs in as a user)", appsetup.KindApp: "application (client secret)"}[p.Kind])
	if p.Existing != nil {
		fmt.Fprintf(out, "Existing app registration: %q (client ID %s); it will be reused\n", p.Existing.DisplayName, p.Existing.ClientID)
	} else {
		fmt.Fprintf(out, "App registration: %q\n", p.DisplayName)
	}
	for _, c := range p.Candidates {
		fmt.Fprintf(out, "Found a similar app registration: %q (client ID %s)\n", c.DisplayName, c.ClientID)
	}
	fmt.Fprintf(out, "Read-only Microsoft Graph permissions (%d): %s\n", len(p.Permissions), strings.Join(p.Permissions, ", "))
	if p.Existing != nil && len(p.Missing) > 0 {
		fmt.Fprintf(out, "  to add: %s\n", strings.Join(p.Missing, ", "))
	}
	if len(p.Actions) == 0 {
		fmt.Fprintln(out, "Everything is already in place; nothing to change.")
		return
	}
	fmt.Fprintln(out, "Nothing existing is removed. This will:")
	for _, a := range p.Actions {
		fmt.Fprintf(out, "  - %s\n", a.Text)
	}
}

// chooseExisting settles adopt-or-create when hand-made candidates exist.
func (a *app) chooseExisting(cmd *cobra.Command, p *appsetup.Plan, f setupFlags) (appsetup.Choice, error) {
	choice := appsetup.Choice{Rename: !f.noRename, CreateNew: f.newApp, Adopt: f.adopt}
	if !p.NeedsChoice() {
		return choice, nil
	}
	if f.adopt != "" && f.newApp {
		return choice, errors.New("--adopt and --new exclude each other")
	}
	if choice.Adopt != "" || choice.CreateNew {
		return choice, nil
	}
	if f.yes {
		return choice, errors.New("a similar app registration exists; say what to do with --adopt <client ID> or --new")
	}
	ctx := contextOrBackground(cmd)
	errOut := cmd.ErrOrStderr()
	for i, c := range p.Candidates {
		fmt.Fprintf(errOut, "  %d) adopt %q (client ID %s)\n", i+1, c.DisplayName, c.ClientID)
	}
	fmt.Fprintf(errOut, "  %d) create a new app registration %q\n", len(p.Candidates)+1, p.DisplayName)
	for {
		fmt.Fprintf(errOut, "Which one? [1] ")
		ans, err := readLineCtx(ctx, cmd.InOrStdin())
		if errors.Is(err, errInterrupted) {
			return choice, err
		}
		if err != nil {
			return choice, errors.New("no answer received; nothing was changed")
		}
		n := 1
		if s := strings.TrimSpace(ans); s != "" {
			if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
				n = 0
			}
		}
		switch {
		case n >= 1 && n <= len(p.Candidates):
			choice.Adopt = p.Candidates[n-1].ObjectID
			if !f.noRename && p.Candidates[n-1].DisplayName != p.DisplayName {
				fmt.Fprintf(errOut, "Rename it to %q? [Y/n] ", p.DisplayName)
				ans, err := readLineCtx(ctx, cmd.InOrStdin())
				if errors.Is(err, errInterrupted) {
					return choice, err
				}
				if err != nil {
					return choice, errors.New("no answer received; nothing was changed")
				}
				if r := strings.ToLower(strings.TrimSpace(ans)); r == "n" || r == "no" {
					choice.Rename = false
				}
			}
			return choice, nil
		case n == len(p.Candidates)+1:
			choice.CreateNew = true
			return choice, nil
		}
		fmt.Fprintf(errOut, "enter a number from 1 to %d\n", len(p.Candidates)+1)
	}
}

func printSetupResult(cmd *cobra.Command, r *appsetup.Result) {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	switch {
	case r.CreatedApp:
		fmt.Fprintf(out, "\ncreated the app registration %q\n", r.DisplayName)
	case r.AdoptedApp:
		fmt.Fprintf(out, "\nadopted the app registration %q\n", r.DisplayName)
	default:
		fmt.Fprintf(out, "\nthe app registration %q is in place\n", r.DisplayName)
	}
	if len(r.AddedPermissions) > 0 && !r.CreatedApp {
		fmt.Fprintf(out, "added permissions: %s\n", strings.Join(r.AddedPermissions, ", "))
	}
	if r.SecretName != "" {
		fmt.Fprintf(out, "client secret %q stored in the vault; expires %s\n", r.SecretName, r.SecretExpires)
	}
	if r.ConsentGranted {
		fmt.Fprintln(out, "administrator consent is granted")
	} else {
		fmt.Fprintf(errOut, "\nwarning: administrator consent was NOT granted: %s\n", r.ConsentError)
		fmt.Fprintf(errOut, "Someone with the right role can grant it by opening:\n  %s\n", r.AdminConsentURL)
	}
	fmt.Fprintf(out, "tenant ID: %s\nclient ID: %s\n", r.TenantID, r.ClientID)
}

// accountAddAuto is `account add --auto`: set the app up, add the account, sign in.
func (a *app) accountAddAuto(cmd *cobra.Command, acct config.Account, f setupFlags) error {
	ctx := contextOrBackground(cmd)
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	o := a.ops()
	if acct.Auth == config.AuthPassword {
		return errors.New("--auto does not support password sign-in; use device_code, browser or client_secret")
	}
	if acct.ClientID != "" {
		return errors.New("--auto creates the app registration itself; drop --client-id (or drop --auto to use your own)")
	}
	if acct.Auth == config.AuthClientSecret && acct.Tenant != "" && (acct.Tenant == "organizations" || acct.Tenant == "common") {
		return errors.New("a client_secret account needs a specific tenant ID or domain")
	}
	// Fail on a bad or taken name before anything is created.
	if err := o.PrepareAddAccount(ops.InputOf(acct)); err != nil {
		return err
	}
	opts := ops.AppSetupOptions{AppOnly: acct.Auth == config.AuthClientSecret, Method: f.method, Tenant: acct.Tenant, Account: acct.Name}
	if opts.Method == "" && !opts.AppOnly {
		opts.Method = acct.Auth
	}
	sess, res, err := a.runAppSetup(cmd, opts, f)
	if err != nil {
		return err
	}
	defer sess.Close()

	acct.Tenant, acct.ClientID = res.TenantID, res.ClientID
	acct.SecretExpires = res.SecretExpires
	in := ops.InputOf(acct)
	in.UseStoredSecret = opts.AppOnly
	if err := o.AddAccount(ctx, in); err != nil {
		if opts.AppOnly {
			a.dropStoredSecret(acct.Name)
		}
		return fmt.Errorf("the app registration is ready (client ID %s) but adding the account failed: %w", res.ClientID, err)
	}
	o.ReloadEngine()
	fmt.Fprintf(out, "\nadded account %q (%s)\n", acct.Name, acct.Auth)

	if !res.ConsentGranted {
		fmt.Fprintf(errOut, "The account is saved. After an administrator has granted consent, run: e5extend account login %s\n", acct.Name)
		return nil
	}
	progress := func(m string) { fmt.Fprintln(errOut, "  "+m) }
	lr, err := o.LoginAfterSetup(ctx, acct.Name, cliUI{cmd}, progress)
	if err != nil {
		return fmt.Errorf("the account is saved, but signing in failed: %w", ops.WithHint(err, acct.Name))
	}
	if lr.Delegated {
		fmt.Fprintf(out, "signed in as %s\n", lr.Account)
	} else {
		fmt.Fprintf(out, "client secret verified for application %s\n", lr.Account)
	}
	printTokenInfo(cmd, lr.TokenInfo)
	return nil
}

// dropStoredSecret removes the vault entry automatic setup made for a name.
func (a *app) dropStoredSecret(name string) {
	cfg, _, err := a.loadConfigOrDefault()
	if err != nil {
		return
	}
	v, err := a.openVault(cfg)
	if err != nil {
		return
	}
	_ = v.Delete(vault.SecretKey(name))
}
