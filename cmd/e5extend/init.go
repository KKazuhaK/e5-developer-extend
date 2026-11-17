package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

const docsURL = "https://github.com/KKazuhaK/e5-developer-extend/blob/main/docs/user/"

func (a *app) isInteractive(in io.Reader) bool {
	if a.interactive != nil {
		return a.interactive(in)
	}
	f, ok := in.(*os.File)
	return ok && isTerminal(f)
}

func (a *app) newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Guided first-time setup: account, sign-in, test round, alerts, background job",
		Long: "Walk through the setup step by step. Every step can be skipped by answering with an empty line\n" +
			"or n, and steps that are already done are skipped automatically. It needs an interactive terminal;\n" +
			"in scripts use the individual commands (config init, account add, account login, service install, ...).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.isInteractive(cmd.InOrStdin()) {
				return errors.New("init needs an interactive terminal. In scripts run the steps yourself:\n" +
					"  e5extend config init\n  e5extend account add <name> --auth ... --tenant ... --client-id ...\n" +
					"  e5extend account login <name>\n  e5extend service install\n  e5extend doctor")
			}
			w := &wizard{a: a, cmd: cmd, ctx: contextOrBackground(cmd), in: cmd.InOrStdin(), out: cmd.OutOrStdout()}
			return w.run()
		},
	}
}

type wizard struct {
	a   *app
	cmd *cobra.Command
	ctx context.Context
	in  io.Reader
	out io.Writer
}

func (w *wizard) say(format string, args ...any) { fmt.Fprintf(w.out, format+"\n", args...) }

func (w *wizard) step(n int, title string) { w.say("\n[%d/9] %s", n, title) }

// ask prints prompt with its default and returns the trimmed answer.
func (w *wizard) ask(prompt, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(w.out, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(w.out, "%s: ", prompt)
	}
	ans, err := readLineCtx(w.ctx, w.in)
	if err != nil {
		if errors.Is(err, errInterrupted) {
			return "", err
		}
		return "", errors.New("the input ended; run e5extend init again to continue where you stopped")
	}
	if ans = strings.TrimSpace(ans); ans == "" {
		return def, nil
	}
	return ans, nil
}

// confirm asks a yes/no question; an empty answer takes the default.
func (w *wizard) confirm(prompt string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		ans, err := w.ask(prompt+" ("+hint+")", "")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(ans) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no", "s", "skip":
			return false, nil
		}
		w.say("please answer y or n")
	}
}

// choose offers numbered options and returns the zero-based pick.
func (w *wizard) choose(prompt string, options []string, def int) (int, error) {
	for i, o := range options {
		w.say("  %d) %s", i+1, o)
	}
	for {
		ans, err := w.ask(prompt, fmt.Sprint(def+1))
		if err != nil {
			return 0, err
		}
		var n int
		if _, err := fmt.Sscanf(ans, "%d", &n); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		w.say("enter a number from 1 to %d", len(options))
	}
}

// sub runs another command in this process with the wizard's input and output.
func (w *wizard) sub(args ...string) error {
	root := newRootCmdWith(w.a)
	root.SilenceErrors, root.SilenceUsage = true, true
	root.SetIn(w.in)
	root.SetOut(w.out)
	root.SetErr(w.cmd.ErrOrStderr())
	root.SetArgs(args)
	return root.ExecuteContext(w.ctx)
}

// try runs a command and shows its error instead of stopping the wizard.
func (w *wizard) try(args ...string) bool {
	err := w.sub(args...)
	if errors.Is(err, errInterrupted) || w.ctx.Err() != nil {
		return false
	}
	if err != nil {
		var ee exitError
		if !errors.As(err, &ee) {
			w.say("  problem: %s", strings.ReplaceAll(err.Error(), "\n", "\n  "))
		}
		return false
	}
	return true
}

func (w *wizard) run() error {
	w.say("e5extend setup. Answer with an empty line to take the default; every step can be skipped.")

	if err := w.stepConfig(); err != nil {
		return err
	}
	if err := w.stepMasterKey(); err != nil {
		return err
	}
	added, err := w.stepAccount()
	if err != nil {
		return err
	}
	if err := w.stepLogin(added); err != nil {
		return err
	}
	w.stepTest()
	if err := w.stepRound(); err != nil {
		return err
	}
	if err := w.stepNotify(); err != nil {
		return err
	}
	if err := w.stepService(); err != nil {
		return err
	}
	w.step(9, "Health check")
	w.try("doctor")
	w.say("\nSetup finished. See %sgetting-started.md for what to expect.", docsURL)
	return nil
}

func (w *wizard) stepConfig() error {
	w.step(1, "Configuration file")
	path, err := paths.ConfigFile()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		w.say("  %s exists; skipped", path)
		return nil
	}
	return w.sub("config", "init")
}

func (w *wizard) stepMasterKey() error {
	w.step(2, "Master key (protects stored secrets)")
	cfg, _, err := w.a.loadConfig()
	if err != nil {
		return err
	}
	_, src, err := vault.LookupMasterKey(cfg.Vault.Backend)
	if err == nil {
		w.say("  found a master key (%s); skipped", src)
		return nil
	}
	if !errors.Is(err, vault.ErrNoMasterKey) {
		return err
	}
	w.say("  no master key yet.")
	options := []string{"the system keyring", "skip"}
	def := 0
	if runtime.GOOS == "linux" {
		// Only Linux can hand a key file to the background job (--key-file), so
		// only there is a key that lives outside the keyring useful.
		options = []string{"the system keyring (desktops)", "generate a key and show it to me (servers without a desktop)", "skip"}
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			def = 1 // a server: the keyring is usually not reachable
		}
	}
	pick, err := w.choose("Where should the key live?", options, def)
	if err != nil {
		return err
	}
	if runtime.GOOS != "linux" && pick == 1 {
		pick = 2 // "skip"
	}
	switch pick {
	case 0:
		w.try("vault", "init")
	case 1:
		key, err := vault.GenerateKey()
		if err != nil {
			return err
		}
		enc := vault.EncodeKey(key)
		w.say("  Your master key (save it somewhere safe; without it stored secrets cannot be read):")
		w.say("    %s", enc)
		w.say("  It is used for the rest of this setup. The background job gets it through --key-file,")
		w.say("  and in other shells provide it as %s.", vault.EnvMasterKey)
		if err := os.Setenv(vault.EnvMasterKey, enc); err != nil {
			return err
		}
	}
	return nil
}

func (w *wizard) stepAccount() ([]string, error) {
	w.step(3, "Account")
	cfg, _, err := w.a.loadConfig()
	if err != nil {
		return nil, err
	}
	if n := len(cfg.Accounts); n > 0 {
		w.say("  %d account(s) already configured; skipped (add more with: e5extend account add)", n)
		return nil, nil
	}
	if ok, err := w.confirm("Add a Microsoft 365 account now?", true); err != nil || !ok {
		return nil, err
	}
	w.say("  The account needs an app registration in your developer tenant (Microsoft Entra).")
	regPick, err := w.choose("App registration", []string{
		"create it automatically (recommended; you sign in once as an administrator of the tenant)",
		"I already have one (manual guide: " + docsURL + "app-registration.md)",
	}, 0)
	if err != nil {
		return nil, err
	}
	if regPick == 0 {
		return w.stepAccountAuto()
	}
	w.say("  Manual setup:")
	w.say("    - delegated sign-in: platform \"Mobile and desktop\", redirect URI http://localhost,")
	w.say("      \"Allow public client flows\" on, plus read-only Graph permissions (Mail.Read, Calendars.Read, ...)")
	w.say("    - app identity: a client secret and application permissions with admin consent")

	name, err := w.ask("Account name (letters, digits, - and _)", "main")
	if err != nil {
		return nil, err
	}
	modes := []string{config.AuthDeviceCode, config.AuthBrowser, config.AuthClientSecret, config.AuthPassword}
	pick, err := w.choose("Sign-in method", []string{
		"device code (works anywhere, also on servers)",
		"browser",
		"client secret (app identity)",
		"username and password (no MFA only; discouraged)",
	}, 0)
	if err != nil {
		return nil, err
	}
	mode := modes[pick]
	tenant, err := w.ask("Tenant (ID or domain; \"organizations\" for delegated sign-in)", "organizations")
	if err != nil {
		return nil, err
	}
	clientID, err := w.ask("Application (client) ID", "")
	if err != nil {
		return nil, err
	}
	args := []string{"account", "add", name, "--auth", mode, "--tenant", tenant, "--client-id", clientID}
	switch mode {
	case config.AuthPassword:
		user, err := w.ask("Sign-in name of the account", "")
		if err != nil {
			return nil, err
		}
		args = append(args, "--username", user)
	case config.AuthClientSecret:
		if exp, err := w.ask("Secret expiry date YYYY-MM-DD (empty to skip; enables expiry alerts)", ""); err != nil {
			return nil, err
		} else if exp != "" {
			args = append(args, "--secret-expires", exp)
		}
	}
	if mode == config.AuthClientSecret || mode == config.AuthPassword {
		w.say("  The next prompt asks for the secret (hidden while typing).")
	}
	if !w.try(args...) {
		w.say("  The account was not added. Fix the problem above and run e5extend init again.")
		return nil, nil
	}
	return []string{name}, nil
}

// stepAccountAuto adds an account whose app registration is created for the
// user. `account add --auto` also signs the account in, so it is not reported
// as freshly added (which would offer a second sign-in).
func (w *wizard) stepAccountAuto() ([]string, error) {
	name, err := w.ask("Account name (letters, digits, - and _)", "main")
	if err != nil {
		return nil, err
	}
	modes := []string{config.AuthDeviceCode, config.AuthBrowser, config.AuthClientSecret}
	pick, err := w.choose("Sign-in method", []string{
		"device code (works anywhere, also on servers)",
		"browser",
		"client secret (app identity, no user sign-in)",
	}, 0)
	if err != nil {
		return nil, err
	}
	args := []string{"account", "add", name, "--auto", "--auth", modes[pick]}
	if modes[pick] == config.AuthClientSecret {
		user, err := w.ask("User whose mailbox and files it reads (sign-in name; empty to pick a random user)", "")
		if err != nil {
			return nil, err
		}
		if user != "" {
			args = append(args, "--username", user)
		}
	}
	w.say("  Next you sign in as an administrator of the tenant, check the plan and confirm.")
	if !w.try(args...) {
		w.say("  The account was not added. Fix the problem above and run e5extend init again, or choose \"I already have one\".")
	}
	return nil, nil
}

func (w *wizard) stepLogin(added []string) error {
	w.step(4, "Sign in")
	cfg, _, err := w.a.loadConfig()
	if err != nil {
		return err
	}
	v, err := w.a.openVault(cfg)
	if err != nil {
		return err
	}
	did := false
	for _, acct := range cfg.Accounts {
		fresh := slices.Contains(added, acct.Name)
		loggedIn := strings.Contains(ops.LocalEntry(v, acct).Status, "logged in") && !strings.Contains(ops.LocalEntry(v, acct).Status, "not logged in")
		if !fresh && (!acct.Delegated() || loggedIn) {
			continue
		}
		did = true
		if ok, err := w.confirm(fmt.Sprintf("Sign in account %s now?", acct.Name), true); err != nil {
			return err
		} else if ok {
			w.try("account", "login", acct.Name)
		}
	}
	if !did {
		w.say("  nothing to do; skipped")
	}
	return nil
}

// stepTest checks that a token can be had silently and counts the usable APIs.
func (w *wizard) stepTest() {
	w.step(5, "Test the sign-in")
	cfg, _, err := w.a.loadConfig()
	if err != nil || len(cfg.Accounts) == 0 {
		w.say("  no accounts; skipped")
		return
	}
	apis, err := catalog.Load()
	if err != nil {
		w.say("  problem: %v", err)
		return
	}
	for _, acct := range cfg.Accounts {
		au, err := w.a.authenticator(cfg, acct)
		if err != nil {
			w.say("  %s: %s", acct.Name, firstLine(withHint(err, acct.Name).Error()))
			continue
		}
		ctx, cancel := context.WithTimeout(w.ctx, time.Minute)
		tok, err := au.Token(ctx)
		cancel()
		if err != nil {
			w.say("  %s: could not get a token silently\n    %s", acct.Name, strings.ReplaceAll(hintOrEmpty(err, acct.Name), "\n", "\n    "))
			continue
		}
		kind := catalog.Delegated
		if !acct.Delegated() {
			kind = catalog.App
		}
		n := len(catalog.Available(apis, kind, tok.Scopes, acct.Categories))
		w.say("  %s: token ok; %d of %d APIs usable with the granted permissions", acct.Name, n, len(apis))
		if n == 0 {
			w.say("    grant more permissions: %sapp-registration.md", docsURL)
		}
	}
}

func hintOrEmpty(err error, name string) string {
	if h := hint(err, name); h != "" {
		return h
	}
	return "see: e5extend doctor"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func (w *wizard) stepRound() error {
	w.step(6, "First round")
	cfg, _, err := w.a.loadConfig()
	if err != nil || len(cfg.Accounts) == 0 {
		w.say("  no accounts; skipped")
		return nil
	}
	ok, err := w.confirm("Run one round of API calls now to see that it works?", true)
	if err != nil || !ok {
		return err
	}
	w.try("run-now")
	return nil
}

func (w *wizard) stepNotify() error {
	w.step(7, "Alerts")
	cfg, path, err := w.a.loadConfig()
	if err != nil {
		return err
	}
	if _, err := w.a.webhook(cfg, path, nil, false).URL(); err == nil {
		w.say("  a webhook is already set; skipped")
		return nil
	}
	ok, err := w.confirm("Send alerts to a webhook (Slack, Discord, WeCom, Feishu, Bark, ...) when something needs you?", false)
	if err != nil || !ok {
		return err
	}
	raw, err := readSecret(w.cmd, "Webhook address (hidden while typing): ")
	if err != nil {
		return err
	}
	if strings.TrimSpace(raw) == "" {
		w.say("  nothing entered; skipped")
		return nil
	}
	if w.try("notify", "set-webhook", strings.TrimSpace(raw)) {
		w.try("notify", "test")
	}
	return nil
}

func (w *wizard) stepService() error {
	w.step(8, "Background job")
	m, err := w.a.serviceManager()
	if err != nil {
		w.say("  not available on this platform: %v", err)
		return nil
	}
	if st, err := m.Status(); err == nil && st.Installed {
		w.say("  already installed; skipped")
		return nil
	}
	ok, err := w.confirm("Install e5extend as a background job that starts at login?", true)
	if err != nil || !ok {
		return err
	}
	args := []string{"service", "install"}
	if runtime.GOOS == "linux" {
		server := os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == ""
		cfg, _, err := w.a.loadConfig()
		if err != nil {
			return err
		}
		_, src, _ := vault.LookupMasterKey(cfg.Vault.Backend)
		if src == vault.SourceEnv {
			// The service does not inherit this shell's environment, so without a
			// key file it could not read the vault at all.
			w.say("  The master key comes from %s, which the background job does not inherit.", vault.EnvMasterKey)
			w.say("  It will be given a key file (--key-file); its only protection is its file permissions (0600).")
			args = append(args, "--key-file")
		} else {
			w.say("  On a server the service cannot reach the system keyring. A key file next to the config is the")
			w.say("  alternative; its only protection is its file permissions (0600).")
			if kf, err := w.confirm("Give the service a key file (--key-file)?", server); err != nil {
				return err
			} else if kf {
				args = append(args, "--key-file")
			}
		}
		if lg, err := w.confirm("Keep it running after you log out (--enable-linger)?", true); err != nil {
			return err
		} else if lg {
			args = append(args, "--enable-linger")
		}
	}
	w.try(args...)
	return nil
}
