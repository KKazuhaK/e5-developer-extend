package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

type schtasks struct{ env Env }

// taskFile is where the imported task definition is kept.
func (m *schtasks) taskFile(dataDir string) string {
	return join(Windows, dataDir, "service", "e5extend-task.xml")
}

// TaskParams are the inputs of the scheduled task definition.
type TaskParams struct {
	Exe     string
	UserID  string // DOMAIN\user
	LogFile string
	// ConfigDir and DataDir, when set, are passed as --config-dir and
	// --data-dir: a task action cannot set environment variables.
	ConfigDir string
	DataDir   string
}

// TaskArguments returns the command line arguments of the task.
func TaskArguments(p TaskParams) string {
	var dirs string
	if p.ConfigDir != "" {
		dirs += "--config-dir " + winQuote(p.ConfigDir) + " "
	}
	if p.DataDir != "" {
		dirs += "--data-dir " + winQuote(p.DataDir) + " "
	}
	return dirs + "run --log-format json --log-file " + winQuote(p.LogFile)
}

func winQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// TaskXML renders the Task Scheduler definition: it starts at logon with no
// time limit (the default would kill the task after 72 hours) and keeps
// running on battery. RestartOnFailure alone is not trusted to bring back a
// process that dies later, so a trigger every 5 minutes (forever) starts it
// again; MultipleInstancesPolicy IgnoreNew and the engine's own lock stop
// duplicates.
func TaskXML(p TaskParams) string {
	x := func(s string) string { return xmlEscape(s) }
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>e5extend - keep a Microsoft 365 E5 developer tenant active</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + x(p.UserID) + `</UserId>
    </LogonTrigger>
    <TimeTrigger>
      <Repetition>
        <Interval>PT5M</Interval>
      </Repetition>
      <StartBoundary>2020-01-01T00:00:00</StartBoundary>
      <Enabled>true</Enabled>
    </TimeTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + x(p.UserID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + x(p.Exe) + `</Command>
      <Arguments>` + x(TaskArguments(p)) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// utf16LE encodes s as UTF-16 little endian with a byte order mark, the form
// Task Scheduler reads most reliably.
func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2+2*len(u))
	b = append(b, 0xFF, 0xFE)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

func (m *schtasks) Plan(o InstallOptions) (Plan, error) {
	if o.Exe == "" || o.DataDir == "" {
		return Plan{}, errors.New("service: executable and data directory are required")
	}
	if o.KeyFile {
		return Plan{}, errors.New("--key-file is only for Linux; on Windows the task runs as you and reads Credential Manager")
	}
	if m.env.User == "" {
		return Plan{}, errors.New("service: cannot tell which Windows user to register the task for")
	}
	tp := TaskParams{Exe: o.Exe, UserID: m.env.User, LogFile: join(Windows, o.DataDir, "engine.log")}
	if o.ExplicitDirs {
		tp.ConfigDir, tp.DataDir = o.ConfigDir, o.DataDir
	}
	xml := TaskXML(tp)
	file := m.taskFile(o.DataDir)
	return Plan{
		Files: []File{{Path: file, Mode: 0o600, Data: utf16LE(xml), Show: xml}},
		Commands: []Command{
			{Args: []string{"schtasks", "/Create", "/TN", TaskName, "/XML", file, "/F"}},
			// /Create /F replaces the definition but leaves a running instance alone.
			{Args: []string{"schtasks", "/End", "/TN", TaskName}, Optional: true, Why: "stop an old instance"},
			{Args: []string{"schtasks", "/Run", "/TN", TaskName}},
		},
	}, nil
}

func (m *schtasks) Install(o InstallOptions) (Result, error) {
	var res Result
	p, err := m.Plan(o)
	if err != nil {
		return res, err
	}
	if err := apply(context.Background(), m.env.Runner, p, false, &res); err != nil {
		return res, err
	}
	res.Warnings = append(res.Warnings, "run log: "+join(Windows, o.DataDir, "engine.log")+" (the task has no console)")
	return res, nil
}

// Uninstall needs the data directory only to delete the kept XML; it removes
// the registration either way.
func (m *schtasks) Uninstall() (Result, error) {
	var res Result
	ctx := context.Background()
	if out, err := m.env.Runner.Run(ctx, "schtasks", "/Query", "/TN", TaskName); err != nil {
		if missing(out, err) {
			res.Actions = append(res.Actions, "nothing to remove: no scheduled task named "+TaskName)
			return res, nil
		}
		return res, fmt.Errorf("ask Task Scheduler: %w", err)
	}
	if _, err := m.env.Runner.Run(ctx, "schtasks", "/End", "/TN", TaskName); err == nil {
		res.Actions = append(res.Actions, "ran schtasks /End /TN "+TaskName)
	}
	if _, err := m.env.Runner.Run(ctx, "schtasks", "/Delete", "/TN", TaskName, "/F"); err != nil {
		return res, err
	}
	res.Actions = append(res.Actions, "ran schtasks /Delete /TN "+TaskName+" /F")
	if m.env.DataDir != "" {
		file := m.taskFile(m.env.DataDir)
		if ok, err := removeFile(file); err != nil {
			res.Warnings = append(res.Warnings, "could not remove "+file+": "+err.Error())
		} else if ok {
			res.Files = append(res.Files, file)
			res.Actions = append(res.Actions, "removed "+file)
		}
	}
	return res, nil
}

var (
	settingsRe = regexp.MustCompile(`(?is)<Settings>(.*?)</Settings>`)
	enabledRe  = regexp.MustCompile(`(?i)<Enabled>\s*(true|false)\s*</Enabled>`)
)

// decodeConsole turns schtasks output into UTF-8 text: /XML output may be
// UTF-16 (with a byte order mark, or recognizable by its NUL bytes).
func decodeConsole(s string) string {
	b := []byte(s)
	le := true
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		b = b[2:]
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		b, le = b[2:], false
	case strings.IndexByte(s, 0) >= 0:
		// UTF-16 without a mark: the NULs sit in the high bytes of ASCII text.
		le = len(b) < 2 || b[1] == 0
	default:
		return strings.TrimPrefix(s, "\ufeff")
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if le {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		} else {
			u = append(u, uint16(b[i+1])|uint16(b[i])<<8)
		}
	}
	return string(utf16.Decode(u))
}

// missing reports whether a failed /Query means "no such task" rather than a
// real problem. schtasks exits 1 for a missing task; the message is localized,
// so only access problems are told apart by their (English) text.
func missing(out Output, err error) bool {
	if err == nil {
		return false
	}
	if out.ExitCode != 1 {
		return false
	}
	msg := strings.ToLower(decodeConsole(out.Stderr + out.Stdout))
	return !strings.Contains(msg, "denied")
}

func (m *schtasks) Status() (Status, error) {
	st := Status{Platform: Windows}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := m.env.Runner.Run(ctx, "schtasks", "/Query", "/TN", TaskName, "/XML")
	if err != nil {
		if missing(out, err) {
			st.Details = append(st.Details, "no scheduled task named "+TaskName)
			return st, nil
		}
		return st, fmt.Errorf("ask Task Scheduler: %w", err)
	}
	st.Installed = true
	st.Enabled = Yes
	if sec := settingsRe.FindStringSubmatch(decodeConsole(out.Stdout)); sec != nil {
		// Only the task-wide Enabled element lives directly under Settings.
		if mm := enabledRe.FindStringSubmatch(sec[1]); mm != nil && strings.EqualFold(mm[1], "false") {
			st.Enabled = No
		}
	}
	// Task Scheduler's own run state is only available as localized text, so
	// the caller decides from the engine's lock and heartbeat instead.
	st.Running = Unknown
	st.Details = append(st.Details, fmt.Sprintf("scheduled task %s is registered", TaskName))
	return st, nil
}

// Start runs the registered task now.
func (m *schtasks) Start() (Result, error) {
	var res Result
	ctx := context.Background()
	if out, err := m.env.Runner.Run(ctx, "schtasks", "/Query", "/TN", TaskName); err != nil {
		if missing(out, err) {
			return res, ErrNotInstalled
		}
		return res, fmt.Errorf("ask Task Scheduler: %w", err)
	}
	if _, err := m.env.Runner.Run(ctx, "schtasks", "/Run", "/TN", TaskName); err != nil {
		return res, fmt.Errorf("run %s: %w", TaskName, err)
	}
	res.Actions = append(res.Actions, "ran schtasks /Run /TN "+TaskName)
	return res, nil
}

// Restart ends the task if it runs and runs it again, so it starts the program
// file as it is now.
func (m *schtasks) Restart() (Result, error) {
	var res Result
	ctx := context.Background()
	if out, err := m.env.Runner.Run(ctx, "schtasks", "/Query", "/TN", TaskName); err != nil {
		if missing(out, err) {
			return res, ErrNotInstalled
		}
		return res, fmt.Errorf("ask Task Scheduler: %w", err)
	}
	// Ending a task that is not running fails; that is fine.
	if _, err := m.env.Runner.Run(ctx, "schtasks", "/End", "/TN", TaskName); err == nil {
		res.Actions = append(res.Actions, "ran schtasks /End /TN "+TaskName)
	}
	if _, err := m.env.Runner.Run(ctx, "schtasks", "/Run", "/TN", TaskName); err != nil {
		return res, fmt.Errorf("run %s: %w", TaskName, err)
	}
	res.Actions = append(res.Actions, "ran schtasks /Run /TN "+TaskName)
	return res, nil
}
