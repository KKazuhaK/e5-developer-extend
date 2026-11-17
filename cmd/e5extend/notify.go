package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
)

// webhook builds the alert sender of this app.
func (a *app) webhook(cfg *config.Config, cfgPath string, log *slog.Logger, retries bool) *notify.Webhook {
	return a.ops().Webhook(cfg, cfgPath, log, retries)
}

func (a *app) newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "notify", Short: "Set up webhook alerts"}
	cmd.AddCommand(a.newNotifySetCmd(), a.newNotifyClearCmd(), a.newNotifyTestCmd())
	return cmd
}

func (a *app) newNotifySetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-webhook [url]",
		Short: "Store the webhook address for alerts (in the encrypted vault)",
		Long: "Store the webhook address in the vault, because it often contains a token.\n" +
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
		Short: "Remove the stored webhook address",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			removed, err := a.ops().ClearWebhook(contextOrBackground(cmd))
			if err != nil {
				return err
			}
			if removed {
				fmt.Fprintln(cmd.OutOrStdout(), "webhook removed; alerts are off")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "no webhook was set")
			}
			return nil
		},
	}
}

func (a *app) newNotifyTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Send a test alert to the webhook",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.ops().TestWebhook(contextOrBackground(cmd)); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "test alert delivered")
			return nil
		},
	}
}
