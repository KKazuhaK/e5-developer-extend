package main

import (
	"errors"
	"net/http"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/doctor"
	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/service"
)

func (a *app) newDoctorCmd() *cobra.Command {
	var offline, asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that everything needed for long-term background running is in place",
		Long: "Run health checks and print one line per check with the command that fixes it.\n" +
			"Exit code: 0 all ok, 1 warnings, 2 failures.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataDir, err := paths.DataDir()
			if err != nil {
				return err
			}
			cfgPath, err := paths.ConfigFile()
			if err != nil {
				return err
			}
			d := doctor.Deps{
				GOOS: runtime.GOOS, DataDir: dataDir, GraphBase: a.graphBase,
				LoadConfig: func() (*config.Config, error) { return config.Load(cfgPath) },
				OpenVault: func(cfg *config.Config) (doctor.Vault, error) {
					return a.openVault(cfg)
				},
				NewAuth: func(cfg *config.Config, acct config.Account) (auth.Authenticator, error) {
					return a.authenticator(cfg, acct)
				},
				HTTP: func(cfg *config.Config) *http.Client {
					proxy := ""
					if cfg != nil {
						proxy = cfg.Network.Proxy
					}
					return auth.NewHTTPClient(proxy)
				},
				Hint: hint,
			}
			if m, err := a.serviceManager(); err == nil {
				d.Service = m.Status
			} else if !errors.Is(err, service.ErrUnsupported) {
				d.Service = func() (service.Status, error) { return service.Status{}, err }
			}
			checks := doctor.Run(contextOrBackground(cmd), d, doctor.Options{Offline: offline})
			out := cmd.OutOrStdout()
			if asJSON {
				if err := doctor.WriteJSON(out, checks); err != nil {
					return err
				}
			} else {
				doctor.WriteText(out, checks, asciiConsole(), offline)
			}
			if code := doctor.ExitCode(checks); code != 0 {
				return exitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "skip the checks that need the network (sign-in, clock, network)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the results as a JSON array")
	return cmd
}

// asciiConsole reports whether the check marks should be plain letters: the
// classic Windows console cannot show them.
func asciiConsole() bool {
	if os.Getenv("E5RENEW_ASCII") != "" {
		return true
	}
	return runtime.GOOS == "windows" && os.Getenv("WT_SESSION") == ""
}
