package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

type systemd struct{ env Env }

func (m *systemd) unitPath() string {
	return join(Linux, m.env.Home, ".config", "systemd", "user", UnitName)
}

// SystemdParams are the inputs of the unit file.
type SystemdParams struct {
	Exe       string
	ConfigDir string
	DataDir   string
	KeyFile   string // path of the key file; empty for none
	// KeyFileEnv hands the key file over as E5EXTEND_MASTER_KEY_FILE instead of
	// LoadCredential=, for systemd versions that predate credentials in user units.
	KeyFileEnv bool
}

// MinCredentialVersion is the first systemd release whose NEWS names
// credentials for user services (v258, encrypted ones); the plain
// LoadCredential= case is confirmed on v259. Older versions get the
// E5EXTEND_MASTER_KEY_FILE fallback.
const MinCredentialVersion = 258

// SystemdUnit renders the user unit.
func SystemdUnit(p SystemdParams) string {
	var sb strings.Builder
	sb.WriteString("[Unit]\n")
	sb.WriteString("Description=e5extend - keep a Microsoft 365 E5 developer tenant active\n")
	// There is no network-online.target in the user manager; the engine
	// retries by itself when the network is down.
	sb.WriteString("# No network ordering: the user manager has no network-online.target and\n")
	sb.WriteString("# the engine retries by itself when the network is down.\n")
	sb.WriteString("\n[Service]\n")
	sb.WriteString("Type=notify\n")
	sb.WriteString("ExecStart=" + unitWord(p.Exe) + " run --log-format json\n")
	sb.WriteString("Restart=on-failure\n")
	sb.WriteString("RestartSec=30\n")
	sb.WriteString("WatchdogSec=120\n")
	sb.WriteString("TimeoutStopSec=30\n")
	switch {
	case p.KeyFile != "" && p.KeyFileEnv:
		sb.WriteString(unitEnv(vault.EnvMasterKeyFile, p.KeyFile))
	case p.KeyFile != "":
		sb.WriteString("LoadCredential=" + vault.CredentialName + ":" + specifiers(p.KeyFile) + "\n")
	}
	sb.WriteString(unitEnv("E5EXTEND_CONFIG_DIR", p.ConfigDir))
	sb.WriteString(unitEnv("E5EXTEND_DATA_DIR", p.DataDir))
	sb.WriteString("\n[Install]\n")
	sb.WriteString("WantedBy=default.target\n")
	return sb.String()
}

// specifiers escapes the % that systemd would read as a specifier.
func specifiers(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// unitWord quotes one word of an ExecStart= line.
func unitWord(s string) string {
	s = strings.ReplaceAll(specifiers(s), "$", "$$")
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func unitEnv(k, v string) string {
	assign := k + "=" + specifiers(v)
	if strings.ContainsAny(assign, " \t\"'\\") {
		assign = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(assign) + `"`
	}
	return "Environment=" + assign + "\n"
}

func (m *systemd) systemctl(args ...string) []string {
	return append([]string{"systemctl", "--user"}, args...)
}

func (m *systemd) Plan(o InstallOptions) (Plan, error) {
	if o.Exe == "" || o.ConfigDir == "" || o.DataDir == "" {
		return Plan{}, errors.New("service: executable and directories are required")
	}
	params := SystemdParams{Exe: o.Exe, ConfigDir: o.ConfigDir, DataDir: o.DataDir}
	var p Plan
	if o.KeyFile {
		if o.MasterKey == nil {
			return Plan{}, errors.New("service: no way to get the master key for --key-file")
		}
		params.KeyFile = join(Linux, o.ConfigDir, KeyFileName)
		params.KeyFileEnv = o.SystemdVersion > 0 && o.SystemdVersion < MinCredentialVersion
		p.Files = append(p.Files, File{Path: params.KeyFile, Mode: 0o600, Secret: true,
			Lazy: func() ([]byte, error) {
				key, err := o.MasterKey()
				if err != nil {
					return nil, fmt.Errorf("get the master key for --key-file: %w", err)
				}
				return []byte(vault.EncodeKey(key) + "\n"), nil
			}})
	}
	p.Files = append([]File{{Path: m.unitPath(), Mode: 0o644, Data: []byte(SystemdUnit(params))}}, p.Files...)
	p.Commands = []Command{
		{Args: m.systemctl("daemon-reload")},
		{Args: m.systemctl("enable", "--now", UnitName)},
		// enable --now does not restart a running unit, so a reinstall would keep the old one.
		{Args: m.systemctl("restart", UnitName), IfInstalled: true, Why: "apply the new unit to the running service"},
	}
	p.After = append(p.After, "check lingering: loginctl show-user "+m.env.User+" -p Linger --value")
	if o.EnableLinger {
		p.After = append(p.After, "if lingering is off: loginctl enable-linger "+m.env.User)
	}
	return p, nil
}

var versionRe = regexp.MustCompile(`(?i)^systemd\s+(\d+)`)

// version returns systemd's major version, or 0 when it cannot be told.
func (m *systemd) version(ctx context.Context) int {
	out, err := m.env.Runner.Run(ctx, "systemctl", "--version")
	if err != nil {
		return 0
	}
	if mm := versionRe.FindStringSubmatch(strings.TrimSpace(out.Stdout)); mm != nil {
		n, _ := strconv.Atoi(mm[1])
		return n
	}
	return 0
}

const busHint = "systemd user services need a login session; if this is an SSH session, log in again " +
	"or run: export XDG_RUNTIME_DIR=/run/user/$(id -u)"

func (m *systemd) Install(o InstallOptions) (Result, error) {
	var res Result
	ctx := context.Background()
	if o.KeyFile && o.SystemdVersion == 0 {
		o.SystemdVersion = m.version(ctx)
		if o.SystemdVersion == 0 {
			// Unknown version: use the form that works on every systemd.
			o.SystemdVersion = 1
			res.Warnings = append(res.Warnings, "could not read the systemd version; the unit hands the key file over as "+
				vault.EnvMasterKeyFile+" instead of LoadCredential=")
		}
		if o.SystemdVersion > 0 && o.SystemdVersion < MinCredentialVersion {
			res.Warnings = append(res.Warnings, fmt.Sprintf("systemd %d is older than %d, the first release documented to pass credentials to user services; "+
				"the unit hands the key file over as %s instead of LoadCredential=", o.SystemdVersion, MinCredentialVersion, vault.EnvMasterKeyFile))
		}
	}
	p, err := m.Plan(o)
	if err != nil {
		return res, err
	}
	if err := apply(ctx, m.env.Runner, p, fileExists(m.unitPath()), &res); err != nil {
		if strings.Contains(err.Error(), "systemctl") {
			err = fmt.Errorf("%w\n%s", err, busHint)
		}
		return res, err
	}
	m.linger(ctx, o, &res)
	return res, nil
}

func (m *systemd) linger(ctx context.Context, o InstallOptions, res *Result) {
	out, err := m.env.Runner.Run(ctx, "loginctl", "show-user", m.env.User, "-p", "Linger", "--value")
	if err != nil {
		res.Warnings = append(res.Warnings, "could not check whether lingering is on ("+firstWord(err)+
			"); without it the service stops when you log out. To turn it on: loginctl enable-linger "+m.env.User)
		return
	}
	if strings.TrimSpace(out.Stdout) != "no" {
		return
	}
	enable := "loginctl enable-linger " + m.env.User
	if o.EnableLinger {
		if _, err := m.env.Runner.Run(ctx, "loginctl", "enable-linger", m.env.User); err == nil {
			res.Actions = append(res.Actions, "ran "+enable)
			return
		}
		res.Manual = append(res.Manual, "sudo "+enable)
		res.Warnings = append(res.Warnings, "could not turn lingering on as this user; run the command below, otherwise the service stops when you log out")
		return
	}
	res.Warnings = append(res.Warnings, "lingering is off for "+m.env.User+": the service stops when you log out. "+
		"Run: "+enable+"   (or install again with --enable-linger)")
}

func firstWord(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func (m *systemd) Uninstall() (Result, error) {
	var res Result
	ctx := context.Background()
	unit := m.unitPath()
	if !fileExists(unit) {
		res.Actions = append(res.Actions, "nothing to remove: "+unit+" does not exist")
		return res, nil
	}
	hadKey := false
	if b, err := os.ReadFile(unit); err == nil {
		hadKey = strings.Contains(string(b), "LoadCredential=") || strings.Contains(string(b), vault.EnvMasterKeyFile+"=")
	}
	// Stopping may fail when the user manager is not reachable; the file is removed regardless.
	if _, err := m.env.Runner.Run(ctx, "systemctl", "--user", "disable", "--now", UnitName); err == nil {
		res.Actions = append(res.Actions, "ran systemctl --user disable --now "+UnitName)
	} else {
		res.Warnings = append(res.Warnings, "could not stop the unit ("+firstWord(err)+"); if it is still running: systemctl --user stop "+UnitName)
	}
	if _, err := removeFile(unit); err != nil {
		return res, err
	}
	res.Files = append(res.Files, unit)
	res.Actions = append(res.Actions, "removed "+unit)
	if _, err := m.env.Runner.Run(ctx, "systemctl", "--user", "daemon-reload"); err == nil {
		res.Actions = append(res.Actions, "ran systemctl --user daemon-reload")
	}
	if hadKey {
		res.Warnings = append(res.Warnings, "the master key file ("+KeyFileName+" in the config directory) was left in place: it may be the only copy of the vault key; delete it yourself if you do not need it")
	}
	return res, nil
}

func (m *systemd) Status() (Status, error) {
	st := Status{Platform: Linux}
	unit := m.unitPath()
	if !fileExists(unit) {
		st.Details = append(st.Details, unit+" does not exist")
		return st, nil
	}
	st.Installed = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r := m.env.Runner
	// is-enabled and is-active exit non-zero for "disabled" and "inactive"; the
	// answer is on stdout either way. Empty stdout means systemctl did not answer.
	en, errEn := r.Run(ctx, "systemctl", "--user", "is-enabled", UnitName)
	ac, errAc := r.Run(ctx, "systemctl", "--user", "is-active", UnitName)
	if strings.TrimSpace(en.Stdout) == "" && strings.TrimSpace(ac.Stdout) == "" {
		err := errEn
		if err == nil {
			err = errAc
		}
		if err == nil {
			err = errors.New("no answer")
		}
		return st, fmt.Errorf("ask systemd: %w\n%s", err, busHint)
	}
	enabled := strings.TrimSpace(en.Stdout)
	active := strings.TrimSpace(ac.Stdout)
	st.Enabled = triOf(enabled == "enabled")
	st.Running = triOf(active == "active")
	st.Details = append(st.Details, "unit: "+enabled+", "+active)
	if out, err := r.Run(ctx, "loginctl", "show-user", m.env.User, "-p", "Linger", "--value"); err == nil {
		st.Linger = triOf(strings.TrimSpace(out.Stdout) == "yes")
	}
	return st, nil
}

// Start starts the installed unit. A unit that is not enabled will not come
// back after a logout or reboot, and without lingering it stops at logout;
// both are reported as warnings.
func (m *systemd) Start() (Result, error) {
	var res Result
	if !fileExists(m.unitPath()) {
		return res, ErrNotInstalled
	}
	ctx := context.Background()
	if out, err := m.env.Runner.Run(ctx, "systemctl", "--user", "start", UnitName); err != nil {
		if strings.Contains(out.Stderr+err.Error(), "masked") {
			return res, fmt.Errorf("%s is masked, so it cannot be started; run: systemctl --user unmask %s", UnitName, UnitName)
		}
		return res, fmt.Errorf("start %s: %w\n%s", UnitName, err, busHint)
	}
	res.Actions = append(res.Actions, "ran systemctl --user start "+UnitName)
	if out, _ := m.env.Runner.Run(ctx, "systemctl", "--user", "is-enabled", UnitName); strings.TrimSpace(out.Stdout) != "enabled" {
		res.Warnings = append(res.Warnings, "the service is not enabled, so it will not start again after you log out or reboot; run: systemctl --user enable "+UnitName)
	}
	m.linger(ctx, InstallOptions{}, &res)
	return res, nil
}

// Restart restarts the user service so it runs the program file as it is now.
func (m *systemd) Restart() (Result, error) {
	var res Result
	if !fileExists(m.unitPath()) {
		return res, ErrNotInstalled
	}
	if out, err := m.env.Runner.Run(context.Background(), "systemctl", "--user", "restart", UnitName); err != nil {
		if strings.Contains(out.Stderr+err.Error(), "masked") {
			return res, fmt.Errorf("%s is masked, so it cannot be restarted; run: systemctl --user unmask %s", UnitName, UnitName)
		}
		return res, fmt.Errorf("restart %s: %w\n%s", UnitName, err, busHint)
	}
	res.Actions = append(res.Actions, "ran systemctl --user restart "+UnitName)
	return res, nil
}
