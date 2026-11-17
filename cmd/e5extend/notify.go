package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
)

func (a *app) newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "notify", Short: "Set up alert channels (Telegram, email, WeCom, Feishu, ...)"}
	cmd.AddCommand(a.newNotifyChannelCmd(), a.newNotifyTestCmd(), a.newNotifyPreviewCmd(), a.newNotifySetCmd(), a.newNotifyClearCmd())
	return cmd
}

func (a *app) newNotifyChannelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "List, add, edit and remove notification channels",
		Long: "A channel is one place alerts go to. Its tokens, passwords and addresses are kept in the encrypted vault;\n" +
			"config.toml only holds the plain settings (chat id, SMTP host, ...). Types: " + strings.Join(config.ChannelTypes, ", ") + ".",
	}
	cmd.AddCommand(a.newChannelListCmd(), a.newChannelAddCmd(), a.newChannelEditCmd(), a.newChannelRemoveCmd())
	return cmd
}

func (a *app) newChannelListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the channels and how their last delivery went",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			chs, err := a.ops().ListChannels()
			if err != nil {
				return withHint(err, "")
			}
			out := cmd.OutOrStdout()
			if len(chs) == 0 {
				fmt.Fprintln(out, "no channels; add one with: e5extend notify channel add <id> --type telegram")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTYPE\tMIN LEVEL\tEVENTS\tLANGUAGE\tSECRETS\tLAST DELIVERY")
			for _, c := range chs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Type, orStr(c.MinLevel, "info"), orStr(strings.Join(c.Events, ","), "all"),
					orStr(c.Language, "auto"), secretSummary(c), lastDelivery(c))
			}
			_ = tw.Flush()
			for _, c := range chs {
				for _, w := range c.Warnings {
					fmt.Fprintf(out, "note: %s: %s\n", c.ID, warningText(w, c.ID))
				}
			}
			return nil
		},
	}
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func secretSummary(c ops.ChannelView) string {
	if len(c.SecretFields) == 0 {
		return "-"
	}
	var parts []string
	for _, f := range c.SecretFields {
		state := "not set"
		if f.Set {
			state = "set"
		}
		parts = append(parts, f.Name+": "+state)
	}
	return strings.Join(parts, ", ")
}

func lastDelivery(c ops.ChannelView) string {
	if c.LastResult == nil {
		return "-"
	}
	when := c.LastResult.At.Local().Format("2006-01-02 15:04")
	if c.LastResult.OK {
		return "ok " + when
	}
	return "failed " + when + ": " + c.LastResult.Error
}

func warningText(w, id string) string {
	switch w {
	case "server_http":
		return "talks to its server over http, so the token travels unencrypted"
	case "email_plain":
		return "the mail connection is not encrypted"
	case "legacy":
		return "stored the way older versions did; run: e5extend notify channel edit " + id + " --secret url   to move it"
	}
	return w
}

// channelFlags are the flags shared by add and edit.
type channelFlags struct {
	typ, minLevel, language, templateFile string
	events                                []string
	chatID, apiBase                       string
	smtpHost, security, username, from    string
	smtpPort                              int
	to                                    []string
	server, topic, format, preset         string
	secrets, clearSecrets                 []string
	clearTemplate                         bool
	allowHTTP                             bool
}

func (f *channelFlags) bind(cmd *cobra.Command, adding bool) {
	fl := cmd.Flags()
	if adding {
		fl.StringVar(&f.typ, "type", "", "channel type: "+strings.Join(config.ChannelTypes, ", "))
	}
	fl.StringVar(&f.minLevel, "min-level", "", "lowest level to send: info, warning or critical")
	fl.StringSliceVar(&f.events, "events", nil, "only send these events, comma separated (default: all)")
	fl.StringVar(&f.language, "language", "", "message language: zh or en (default: follow the app)")
	fl.StringVar(&f.templateFile, "template-file", "", "file with a custom Go text/template for the message")
	fl.BoolVar(&f.clearTemplate, "clear-template", false, "go back to the built-in template")
	fl.StringVar(&f.chatID, "chat-id", "", "telegram: chat id")
	fl.StringVar(&f.apiBase, "api-base", "", "telegram: Bot API address when you run a reverse proxy")
	fl.StringVar(&f.preset, "preset", "", "email: fill in the server for gmail, outlook, qq or 163")
	fl.StringVar(&f.smtpHost, "smtp-host", "", "email: SMTP server")
	fl.IntVar(&f.smtpPort, "smtp-port", 0, "email: SMTP port (default by security: 587, 465, 25)")
	fl.StringVar(&f.security, "security", "", "email: starttls (default), tls or none")
	fl.StringVar(&f.username, "username", "", "email: user name for the SMTP server")
	fl.StringVar(&f.from, "from", "", "email: sender address")
	fl.StringSliceVar(&f.to, "to", nil, "email: recipient address (repeat or comma separated)")
	fl.StringVar(&f.server, "server", "", "bark/ntfy: server address (default: the public one)")
	fl.StringVar(&f.topic, "topic", "", "ntfy: topic")
	fl.StringVar(&f.format, "format", "", "webhook: json (default) or template")
	if !adding {
		fl.StringSliceVar(&f.secrets, "secret", nil, "enter this secret field again (read without echo); repeatable")
		fl.BoolVar(&f.allowHTTP, "allow-http", true, "webhook: --allow-http=false removes the exception that lets its address be http")
		fl.StringSliceVar(&f.clearSecrets, "clear-secret", nil, "remove this optional secret field; repeatable")
	}
}

// apply copies the flags that were given into in.
func (f *channelFlags) apply(cmd *cobra.Command, in *ops.ChannelInput) error {
	changed := cmd.Flags().Changed
	if changed("preset") {
		p, ok := notify.EmailPresetByName(f.preset)
		if !ok {
			return fmt.Errorf("unknown preset %q (use gmail, outlook, qq or 163)", f.preset)
		}
		if !changed("smtp-host") {
			in.SMTPHost = p.Host
		}
		if !changed("smtp-port") {
			in.SMTPPort = p.Port
		}
		if !changed("security") {
			in.Security = p.Security
		}
	}
	set := func(flag string, dst *string, v string) {
		if changed(flag) {
			*dst = v
		}
	}
	set("min-level", &in.MinLevel, f.minLevel)
	set("language", &in.Language, f.language)
	set("chat-id", &in.ChatID, f.chatID)
	set("api-base", &in.APIBase, f.apiBase)
	set("smtp-host", &in.SMTPHost, f.smtpHost)
	set("security", &in.Security, f.security)
	set("username", &in.Username, f.username)
	set("from", &in.From, f.from)
	set("server", &in.Server, f.server)
	set("topic", &in.Topic, f.topic)
	set("format", &in.Format, f.format)
	if changed("smtp-port") {
		in.SMTPPort = f.smtpPort
	}
	if changed("events") {
		in.Events = f.events
	}
	if changed("to") {
		in.To = f.to
	}
	if changed("allow-http") {
		if f.allowHTTP {
			return errors.New("--allow-http can only be turned off (--allow-http=false); use an https address instead")
		}
		in.AllowHTTP = false
	}
	if f.clearTemplate {
		in.Template = ""
	}
	if f.templateFile != "" {
		b, err := os.ReadFile(f.templateFile)
		if err != nil {
			return fmt.Errorf("read the template: %w", err)
		}
		in.Template = string(b)
	}
	return nil
}

// promptSecrets reads the secret fields of in.Type named in want, one line
// each (without echo on a terminal, one line of stdin otherwise). Optional
// fields may be left empty.
func promptSecrets(cmd *cobra.Command, in *ops.ChannelInput, want func(config.SecretField) bool) error {
	if in.Secrets == nil {
		in.Secrets = map[string]string{}
	}
	for _, f := range config.ChannelSecrets(in.Type) {
		if !want(f) {
			continue
		}
		label := f.Name
		if !f.Required {
			label += " (optional, Enter to skip)"
		}
		raw, err := readSecret(cmd, label+": ")
		if err != nil {
			if !f.Required && errors.Is(err, io.EOF) {
				continue
			}
			return err
		}
		if raw = strings.TrimSpace(raw); raw != "" {
			in.Secrets[f.Name] = raw
		}
	}
	return nil
}

func printValidation(cmd *cobra.Command, bad []ops.ValidationError) error {
	var lines []string
	for _, b := range bad {
		lines = append(lines, "  "+b.Field+": "+b.Message)
	}
	return fmt.Errorf("the channel is not valid:\n%s", strings.Join(lines, "\n"))
}

func (a *app) newChannelAddCmd() *cobra.Command {
	var f channelFlags
	cmd := &cobra.Command{
		Use:   "add <id> --type <type>",
		Short: "Add a notification channel",
		Long: "Add a channel. The id is a name you choose (a-z, 0-9 and -). Secrets (bot token, password, webhook address, sign secret)\n" +
			"are read without echo from the terminal, or one line each from stdin, in this order for each type:\n" +
			"  telegram: bot_token   email: password   wecom/discord/slack/webhook: url   feishu/dingtalk: url, secret\n" +
			"  bark: device_key   ntfy: token   serverchan: sendkey\n\n" +
			"Examples:\n  e5extend notify channel add tg --type telegram --chat-id 123456789\n" +
			"  e5extend notify channel add mail --type email --preset gmail --username me@gmail.com --from me@gmail.com --to me@gmail.com",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.typ == "" {
				return fmt.Errorf("--type is required (use %s)", strings.Join(config.ChannelTypes, ", "))
			}
			in := ops.ChannelInput{ID: args[0], Type: f.typ}
			if err := f.apply(cmd, &in); err != nil {
				return err
			}
			if err := promptSecrets(cmd, &in, func(config.SecretField) bool { return true }); err != nil {
				return err
			}
			bad, err := a.ops().SaveChannel(contextOrBackground(cmd), in, true)
			if err != nil {
				return withHint(err, "")
			}
			if len(bad) > 0 {
				return printValidation(cmd, bad)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "channel %q added (secrets are stored in the vault and not shown again)\n", in.ID)
			fmt.Fprintf(out, "next: e5extend notify test %s\n", in.ID)
			return nil
		},
	}
	f.bind(cmd, true)
	return cmd
}

func (a *app) newChannelEditCmd() *cobra.Command {
	var f channelFlags
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Change a channel; only the flags you give are changed",
		Long: "Change the settings of a channel. Secrets are kept unless you ask to enter them again with --secret <field>\n" +
			"(for example --secret bot_token) or remove an optional one with --clear-secret <field>.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o := a.ops()
			in, err := o.GetChannel(args[0])
			if err != nil {
				return withHint(err, "")
			}
			if err := f.apply(cmd, &in); err != nil {
				return err
			}
			valid := map[string]bool{}
			for _, s := range config.ChannelSecrets(in.Type) {
				valid[s.Name] = true
			}
			for _, name := range append(slices.Clone(f.secrets), f.clearSecrets...) {
				if !valid[name] {
					return fmt.Errorf("a %s channel has no secret %q", in.Type, name)
				}
			}
			if err := promptSecrets(cmd, &in, func(s config.SecretField) bool { return slices.Contains(f.secrets, s.Name) }); err != nil {
				return err
			}
			in.ClearSecrets = f.clearSecrets
			bad, err := o.SaveChannel(contextOrBackground(cmd), in, false)
			if err != nil {
				return withHint(err, "")
			}
			if len(bad) > 0 {
				return printValidation(cmd, bad)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "channel %q updated\n", in.ID)
			return nil
		},
	}
	f.bind(cmd, false)
	return cmd
}

func (a *app) newChannelRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove a channel and its stored secrets",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			removed, err := a.ops().RemoveChannel(contextOrBackground(cmd), args[0])
			if err != nil {
				return withHint(err, "")
			}
			if !removed {
				return fmt.Errorf("no channel named %q\nrun: e5extend notify channel list", args[0])
			}
			fmt.Fprintf(cmd.OutOrStdout(), "channel %q removed\n", args[0])
			return nil
		},
	}
}

func (a *app) newNotifySetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-webhook [url]",
		Short: "Store a generic webhook address (same as: channel add webhook --type webhook)",
		Long: "Store the webhook address in the vault, because it often contains a token. This sets the channel \"webhook\"\n" +
			"of type webhook; it is an alias kept for older setups.\n" +
			"Without an argument the address is read without echo from the terminal, or one line from stdin.\n" +
			"A plaintext notify.webhook_url in config.toml is removed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw string
			if len(args) == 1 {
				raw = args[0]
			} else {
				var err error
				if raw, err = readSecret(cmd, "Webhook address: "); err != nil {
					return err
				}
			}
			raw = strings.TrimSpace(raw)
			cleared, err := a.ops().SetWebhook(contextOrBackground(cmd), raw)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "webhook address stored in the vault (it is not shown again)")
			if cleared {
				fmt.Fprintln(out, "removed notify.webhook_url from config.toml")
			}
			fmt.Fprintln(out, "next: e5extend notify test")
			return nil
		},
	}
}

func (a *app) newNotifyClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove the channel \"webhook\" (same as: channel remove webhook)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			removed, err := a.ops().ClearWebhook(contextOrBackground(cmd))
			if err != nil {
				return err
			}
			if removed {
				if chs, _ := a.ops().ListChannels(); len(chs) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "webhook removed; alerts are off")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "webhook removed")
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "no webhook was set")
			}
			return nil
		},
	}
}

func (a *app) newNotifyTestCmd() *cobra.Command {
	var event string
	var digest int
	cmd := &cobra.Command{
		Use:   "test [id]",
		Short: "Send a test message to every channel, or to one",
		Long: "Send a test message. --event <kind> sends the example of that event (one of " + strings.Join(notify.SampleKinds(), ", ") + "),\n" +
			"--digest N a quiet-hours summary of N example items (a large N shows how long messages are split).\n" +
			"Samples are marked as tests, take the real sending path, and change no alert state or cooldown.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			res, err := a.ops().TestChannelsWith(contextOrBackground(cmd), id, ops.TestOptions{Event: event, Digest: digest})
			if err != nil {
				return withHint(err, "")
			}
			out := cmd.OutOrStdout()
			var failed []string
			for _, r := range res {
				if r.OK {
					fmt.Fprintf(out, "%s: test alert delivered\n", r.ID)
				} else {
					failed = append(failed, "  "+r.ID+": "+r.Error)
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("%d of %d channels did not deliver the test alert:\n%s", len(failed), len(res), strings.Join(failed, "\n"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&event, "event", "", "send the example of this event kind")
	cmd.Flags().IntVar(&digest, "digest", 0, "send a sample quiet-hours digest of N items")
	return cmd
}

func (a *app) newNotifyPreviewCmd() *cobra.Command {
	var typ, lang, tmplFile, format string
	cmd := &cobra.Command{
		Use:   "preview <event>",
		Short: "Show what a message looks like, with example data; nothing is sent",
		Long: "Render an example event: one of " + strings.Join(notify.SampleKinds(), ", ") + ".\n" +
			"Without --type every configured channel is shown with its own language and template (or telegram when there is none).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o := a.ops()
			out := cmd.OutOrStdout()
			type job struct{ id, typ, lang, tmpl, format string }
			var jobs []job
			custom := ""
			if tmplFile != "" {
				b, err := os.ReadFile(tmplFile)
				if err != nil {
					return fmt.Errorf("read the template: %w", err)
				}
				custom = string(b)
			}
			if typ != "" {
				jobs = append(jobs, job{typ: typ, lang: lang, tmpl: custom, format: format})
			} else {
				chs, err := o.ListChannels()
				if err != nil {
					return withHint(err, "")
				}
				for _, c := range chs {
					l := c.Language
					if lang != "" {
						l = lang
					}
					jobs = append(jobs, job{id: c.ID, typ: c.Type, lang: l, tmpl: c.Template, format: c.Format})
				}
				if len(jobs) == 0 {
					jobs = append(jobs, job{typ: config.ChannelTelegram, lang: lang})
				}
			}
			for i, j := range jobs {
				p, err := o.PreviewMessage(j.typ, j.lang, j.tmpl, j.format, args[0])
				if err != nil {
					return err
				}
				if i > 0 {
					fmt.Fprintln(out)
				}
				name := j.typ
				if j.id != "" {
					name = j.id + " (" + j.typ + ")"
				}
				fmt.Fprintf(out, "--- %s, %s ---\n", name, p.Format)
				if p.Title != "" {
					fmt.Fprintf(out, "title: %s\n", p.Title)
				}
				fmt.Fprintln(out, p.Body)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&typ, "type", "", "channel type to render for")
	cmd.Flags().StringVar(&lang, "language", "", "zh or en")
	cmd.Flags().StringVar(&tmplFile, "template-file", "", "render a custom template from this file")
	cmd.Flags().StringVar(&format, "format", "", "webhook: json or template")
	return cmd
}
