package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
)

type launchd struct{ env Env }

func (m *launchd) plistPath() string {
	return join(Darwin, m.env.Home, "Library", "LaunchAgents", Label+".plist")
}

func (m *launchd) logPath() string {
	return join(Darwin, m.env.Home, "Library", "Logs", "e5extend", "engine.log")
}

func (m *launchd) domain() string { return "gui/" + m.env.UID }

func (m *launchd) target() string { return m.domain() + "/" + Label }

// LaunchdParams are the inputs of the LaunchAgent plist.
type LaunchdParams struct {
	Exe     string
	LogFile string            // the engine's own rotating run log (--log-file)
	LogPath string            // StandardOut/ErrorPath: only early crash output ends up here
	Env     map[string]string // EnvironmentVariables; may be empty
}

// LaunchdPlist renders the LaunchAgent. ProcessType is Standard, not
// Background: Background makes the system throttle timers, which the engine
// relies on.
func LaunchdPlist(p LaunchdParams) string {
	str := func(s string) string { return "<string>" + html.EscapeString(s) + "</string>" }
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	` + str(Label) + `
	<key>ProgramArguments</key>
	<array>
		` + str(p.Exe) + `
		<string>run</string>
		<string>--log-format</string>
		<string>json</string>
		<string>--log-file</string>
		` + str(p.LogFile) + `
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>ProcessType</key>
	<string>Standard</string>
	<key>StandardOutPath</key>
	` + str(p.LogPath) + `
	<key>StandardErrorPath</key>
	` + str(p.LogPath) + `
`)
	if len(p.Env) > 0 {
		keys := make([]string, 0, len(p.Env))
		for k := range p.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range keys {
			sb.WriteString("\t\t<key>" + html.EscapeString(k) + "</key>\n\t\t" + str(p.Env[k]) + "\n")
		}
		sb.WriteString("\t</dict>\n")
	}
	sb.WriteString("</dict>\n</plist>\n")
	return sb.String()
}

func (m *launchd) Plan(o InstallOptions) (Plan, error) {
	if o.Exe == "" {
		return Plan{}, errors.New("service: executable is required")
	}
	if o.KeyFile {
		return Plan{}, errors.New("--key-file is only for Linux; on macOS the job runs in your login session and reads the keychain")
	}
	if o.Headless {
		return Plan{}, errors.New(headlessWindowsOnly + "; macOS has no equivalent: a LaunchAgent runs in your login session")
	}
	if o.DataDir == "" {
		return Plan{}, errors.New("service: data directory is required")
	}
	params := LaunchdParams{Exe: o.Exe, LogPath: m.logPath(), LogFile: join(Darwin, o.DataDir, "engine.log")}
	if o.ExplicitDirs {
		params.Env = map[string]string{"E5EXTEND_CONFIG_DIR": o.ConfigDir, "E5EXTEND_DATA_DIR": o.DataDir}
	}
	return Plan{
		Dirs:  []string{join(Darwin, m.env.Home, "Library", "Logs", "e5extend")},
		Files: []File{{Path: m.plistPath(), Mode: 0o644, Data: []byte(LaunchdPlist(params))}},
		Commands: []Command{
			{Args: []string{"launchctl", "bootout", m.target()}, Optional: true, IfInstalled: true, Settle: settleWait, Why: "replace the loaded job"},
			// A disabled label cannot be bootstrapped, so enable comes first.
			{Args: []string{"launchctl", "enable", m.target()}},
			{Args: []string{"launchctl", "bootstrap", m.domain(), m.plistPath()}, Retry: true, Wait: retryWait},
		},
	}, nil
}

const guiHint = "launchd user agents need a logged-in desktop session (the gui/<uid> domain); " +
	"run this from Terminal on the Mac itself, not over SSH without a login"

func (m *launchd) Install(o InstallOptions) (Result, error) {
	var res Result
	p, err := m.Plan(o)
	if err != nil {
		return res, err
	}
	installed := fileExists(m.plistPath())
	if err := apply(context.Background(), m.env.Runner, p, installed, &res); err != nil {
		if strings.Contains(err.Error(), "bootstrap") {
			err = fmt.Errorf("%w\n%s", err, guiHint)
		}
		return res, err
	}
	res.Warnings = append(res.Warnings, "run log: "+join(Darwin, o.DataDir, "engine.log")+" (early crash output: "+m.logPath()+")")
	return res, nil
}

func (m *launchd) Uninstall() (Result, error) {
	var res Result
	plist := m.plistPath()
	if !fileExists(plist) {
		res.Actions = append(res.Actions, "nothing to remove: "+plist+" does not exist")
		return res, nil
	}
	if _, err := m.env.Runner.Run(context.Background(), "launchctl", "bootout", m.target()); err == nil {
		res.Actions = append(res.Actions, "ran launchctl bootout "+m.target())
	} else {
		res.Warnings = append(res.Warnings, "could not unload the job ("+firstWord(err)+"); it may not have been loaded")
	}
	if _, err := removeFile(plist); err != nil {
		return res, err
	}
	res.Files = append(res.Files, plist)
	res.Actions = append(res.Actions, "removed "+plist)
	return res, nil
}

func (m *launchd) Status() (Status, error) {
	st := Status{Platform: Darwin}
	plist := m.plistPath()
	if !fileExists(plist) {
		st.Details = append(st.Details, plist+" does not exist")
		return st, nil
	}
	st.Installed = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := m.env.Runner.Run(ctx, "launchctl", "print", m.target())
	if err != nil {
		// Not loaded: the plist exists but launchd does not know the job.
		st.Enabled, st.Running = No, No
		st.Details = append(st.Details, "the job is not loaded (launchctl print failed)")
		return st, nil
	}
	st.Enabled = Yes
	st.Running = triOf(strings.Contains(out.Stdout, "state = running"))
	if dis, err := m.env.Runner.Run(ctx, "launchctl", "print-disabled", m.domain()); err == nil {
		if strings.Contains(dis.Stdout, `"`+Label+`" => disabled`) || strings.Contains(dis.Stdout, `"`+Label+`" => true`) {
			st.Enabled = No
		}
	}
	st.Details = append(st.Details, "loaded in "+m.domain())
	return st, nil
}

// Start starts the job. A loaded job that is only disabled is enabled and
// kicked again; a job that is not loaded at all is bootstrapped last.
func (m *launchd) Start() (Result, error) {
	var res Result
	if !fileExists(m.plistPath()) {
		return res, ErrNotInstalled
	}
	ctx := context.Background()
	run := func(args ...string) error {
		_, err := m.env.Runner.Run(ctx, "launchctl", args...)
		if err == nil {
			res.Actions = append(res.Actions, "ran launchctl "+strings.Join(args, " "))
		}
		return err
	}
	if run("kickstart", m.target()) == nil {
		return res, nil
	}
	if err := run("enable", m.target()); err != nil {
		return res, fmt.Errorf("enable %s: %w", Label, err)
	}
	if run("kickstart", m.target()) == nil {
		return res, nil
	}
	if err := run("bootstrap", m.domain(), m.plistPath()); err != nil {
		return res, fmt.Errorf("load %s: %w", Label, err)
	}
	return res, nil
}
