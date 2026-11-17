package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// BootDelay is how long after boot the headless task starts (ISO 8601): the
// network and the profile service get a moment. The engine retries by itself
// when the network is still down.
const BootDelay = "PT30S"

type schtasks struct{ env Env }

func (m *schtasks) writeKey(path string, key []byte) error {
	if m.env.WriteKeyFile != nil {
		return m.env.WriteKeyFile(path, key)
	}
	return vault.WriteDPAPIKeyFile(path, key)
}

func (m *schtasks) auditKey(path string) vault.KeyFileAudit {
	if m.env.AuditKeyFile != nil {
		return m.env.AuditKeyFile(path)
	}
	return vault.AuditDPAPIKeyFile(path)
}

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
	// Headless runs the task whether or not the user is signed in (logon type
	// S4U, no stored password) and also starts it at boot. KeyFile is then the
	// DPAPI key file, handed over as --dpapi-key-file.
	Headless bool
	KeyFile  string
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
	if p.KeyFile != "" {
		dirs += "--dpapi-key-file " + winQuote(p.KeyFile) + " "
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
//
// A headless task (p.Headless) differs in two places: the principal's
// LogonType is S4U ("run whether the user is logged on or not", no password
// stored) instead of InteractiveToken, and a BootTrigger with a short delay
// starts it when the machine boots with nobody signed in.
func TaskXML(p TaskParams) string {
	x := func(s string) string { return xmlEscape(s) }
	logonType, boot := "InteractiveToken", ""
	if p.Headless {
		logonType = "S4U"
		boot = `    <BootTrigger>
      <Enabled>true</Enabled>
      <Delay>` + BootDelay + `</Delay>
    </BootTrigger>
`
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>e5extend - keep a Microsoft 365 E5 developer tenant active</Description>
  </RegistrationInfo>
  <Triggers>
` + boot + `    <LogonTrigger>
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
      <LogonType>` + logonType + `</LogonType>
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
		return Plan{}, errors.New("--key-file is only for Linux; on Windows the task runs as you and reads Credential Manager " +
			"(for a task that runs without anyone signed in, use --headless)")
	}
	if m.env.User == "" {
		return Plan{}, errors.New("service: cannot tell which Windows user to register the task for")
	}
	tp := TaskParams{Exe: o.Exe, UserID: m.env.User, LogFile: join(Windows, o.DataDir, "engine.log")}
	// A headless task runs without a profile, so it must not depend on how the
	// directories are derived from the environment: pass them explicitly.
	if o.ExplicitDirs || o.Headless {
		tp.ConfigDir, tp.DataDir = o.ConfigDir, o.DataDir
	}
	var files []File
	if o.Headless {
		if o.ConfigDir == "" {
			return Plan{}, errors.New("service: the configuration directory is required for --headless")
		}
		if o.MasterKey == nil {
			return Plan{}, errors.New("service: no way to get the master key for --headless")
		}
		tp.Headless = true
		tp.KeyFile = m.keyFilePath(o.ConfigDir)
		files = append(files, File{Path: tp.KeyFile, Secret: true,
			Note: "(the master key, encrypted with DPAPI at machine scope; access limited to you, SYSTEM and Administrators)",
			Lazy: func() ([]byte, error) {
				key, err := o.MasterKey()
				if err != nil {
					return nil, fmt.Errorf("get the master key for --headless: %w", err)
				}
				return key, nil
			},
			Write: m.writeKey})
	}
	xml := TaskXML(tp)
	file := m.taskFile(o.DataDir)
	files = append(files, File{Path: file, Mode: 0o600, Data: utf16LE(xml), Show: xml})
	return Plan{
		Files: files,
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
	keyPath := m.keyFilePath(o.ConfigDir)
	keyExisted := o.Headless && fileExists(keyPath)
	if err := apply(context.Background(), m.env.Runner, p, false, &res); err != nil {
		registered := slices.Contains(res.Actions, "ran "+createCommand(p))
		if o.Headless && !registered && !keyExisted {
			// Do not leave a freshly written secret behind for a task that
			// was never registered.
			_, _ = removeFile(keyPath)
			res.Files = removeString(res.Files, keyPath)
			res.Actions = removeString(res.Actions, "wrote "+keyPath)
		}
		return res, m.installFailure(o, &res, err, registered)
	}
	if !o.Headless && o.ConfigDir != "" {
		// A headless install made this file; the interactive task does not use
		// it, and a secret copy nobody needs should not stay behind.
		switch ok, err := removeFile(keyPath); {
		case err != nil:
			res.Warnings = append(res.Warnings, "the headless key file "+keyPath+" is no longer used but could not be removed ("+err.Error()+"); delete it yourself")
		case ok:
			res.Files = append(res.Files, keyPath)
			res.Actions = append(res.Actions, "removed the headless key file "+keyPath+" (this task does not use it)")
		}
	}
	res.Warnings = append(res.Warnings, "run log: "+join(Windows, o.DataDir, "engine.log")+" (the task has no console)")
	if o.Headless {
		res.Actions = append(res.Actions, "the task runs whether or not anyone is signed in (S4U), and starts "+BootDelay+" after boot")
		res.Warnings = append(res.Warnings, "headless tasks cannot use network resources that need your Windows credentials "+
			"(file shares, Kerberos); outbound HTTPS to Microsoft Graph is not affected")
	}
	return res, nil
}

func removeString(list []string, s string) []string {
	out := list[:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// keyFilePath is where a headless install keeps the DPAPI key file.
func (m *schtasks) keyFilePath(configDir string) string {
	return join(Windows, configDir, vault.DPAPIKeyFileName)
}

// consoleExe turns the console-less e5extend-bg.exe into the e5extend.exe next
// to it, the program to type in an elevated prompt (the other one prints nothing).
func consoleExe(exe string) string {
	if strings.EqualFold(winBase(exe), BackgroundExeName) {
		return join(Windows, winDir(exe), "e5extend.exe")
	}
	return exe
}

// ElevatedInstallCommand is the command to run from a prompt opened with
// "Run as administrator", as the same user.
func ElevatedInstallCommand(o InstallOptions) string {
	var dirs string
	if o.ExplicitDirs || o.Headless {
		dirs = " --config-dir " + winQuote(o.ConfigDir) + " --data-dir " + winQuote(o.DataDir)
	}
	cmd := winQuote(consoleExe(o.Exe)) + dirs + " service install"
	if o.Headless {
		cmd += " --headless"
	}
	return cmd
}

// createCommand is the schtasks /Create command line of a plan.
func createCommand(p Plan) string {
	for _, c := range p.Commands {
		if len(c.Args) > 1 && c.Args[1] == "/Create" {
			return strings.Join(c.Args, " ")
		}
	}
	return ""
}

// Codes that mean "the account may not log on as a batch job": Win32 1385
// (ERROR_LOGON_TYPE_NOT_GRANTED) and its HRESULT.
var batchRightRe = regexp.MustCompile(`(?i)\b1385\b|0x80070569|80070569`)

// installFailure explains a failed install. Registered says the task exists
// although a later step failed. Access denied can happen in any mode and
// gets the elevated command; a headless install that is not elevated gets it
// too, and a refused S4U logon gets a hint about the batch logon right.
func (m *schtasks) installFailure(o InstallOptions, res *Result, err error, registered bool) error {
	low := strings.ToLower(err.Error())
	notElevated := o.Headless && m.env.Elevated != nil && !m.env.Elevated()
	switch {
	case registered:
		res.Manual = append(res.Manual, "schtasks /Run /TN "+TaskName+"   (or sign out and in, or restart the computer)")
		return fmt.Errorf("the task %s is registered but did not start: %w", TaskName, err)
	case strings.Contains(low, "denied") || notElevated:
		res.Manual = append(res.Manual,
			"open a prompt with Run as administrator (as this same user, "+m.env.User+") and run: "+ElevatedInstallCommand(o))
		if o.Headless {
			return fmt.Errorf("registering a headless task needs an elevated (administrator) prompt: %w", err)
		}
		return fmt.Errorf("Task Scheduler refused (access denied); try from an elevated prompt: %w", err)
	case o.Headless && (batchRightRe.MatchString(low) || strings.Contains(low, "logon failure") ||
		strings.Contains(low, "logon type") || strings.Contains(low, "batch job")):
		return fmt.Errorf("%w (the account needs the \"Log on as a batch job\" right: Local Security Policy > Local Policies > "+
			"User Rights Assignment; administrators have it by default)", err)
	}
	return err
}

// Uninstall needs the data directory only to delete the kept XML; it removes
// the registration either way.
func (m *schtasks) Uninstall() (Result, error) {
	var res Result
	ctx := context.Background()
	out, err := m.env.Runner.Run(ctx, "schtasks", "/Query", "/TN", TaskName, "/XML")
	if err != nil {
		if missing(out, err) {
			res.Actions = append(res.Actions, "nothing to remove: no scheduled task named "+TaskName)
			// A key file can outlive its task; only --headless ever creates one.
			m.removeKeyFiles("", &res)
			return res, nil
		}
		return res, fmt.Errorf("ask Task Scheduler: %w", err)
	}
	def := parseTask(decodeConsole(out.Stdout))
	if _, err := m.env.Runner.Run(ctx, "schtasks", "/End", "/TN", TaskName); err == nil {
		res.Actions = append(res.Actions, "ran schtasks /End /TN "+TaskName)
	}
	if _, err := m.env.Runner.Run(ctx, "schtasks", "/Delete", "/TN", TaskName, "/F"); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "denied") {
			res.Manual = append(res.Manual, "from a prompt opened with Run as administrator: schtasks /Delete /TN "+TaskName+" /F")
		}
		return res, err
	}
	m.removeKeyFiles(def.KeyFile, &res)
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

// removeKeyFiles deletes the DPAPI key file(s) a headless install created:
// the one the task definition names (when it has the file's usual name, so a
// doctored task can never make us delete another file) and the one next to the
// configuration. The file is only ever created by --headless, so it goes
// whatever the task's logon type is now (a plain install over a headless one
// removes it too, but an interrupted switch may leave it).
func (m *schtasks) removeKeyFiles(fromTask string, res *Result) {
	var paths []string
	if fromTask != "" {
		if !strings.EqualFold(winBase(fromTask), vault.DPAPIKeyFileName) {
			res.Warnings = append(res.Warnings, "the task named a key file that is not "+vault.DPAPIKeyFileName+"; it was left alone")
		} else {
			paths = append(paths, fromTask)
		}
	}
	if m.env.ConfigDir != "" {
		if p := m.keyFilePath(m.env.ConfigDir); !slices.ContainsFunc(paths, func(q string) bool { return strings.EqualFold(p, q) }) {
			paths = append(paths, p)
		}
	}
	removed := false
	for _, path := range paths {
		switch ok, err := removeFile(path); {
		case err != nil:
			res.Warnings = append(res.Warnings, "could not remove the key file "+path+": "+err.Error()+"; delete it yourself")
		case ok:
			removed = true
			res.Files = append(res.Files, path)
			res.Actions = append(res.Actions, "removed the headless key file "+path)
		}
	}
	if removed {
		res.Warnings = append(res.Warnings, "the key file was only a copy for the headless task: your master key itself "+
			"(Credential Manager, or wherever you keep "+vault.EnvMasterKey+"), the vault and the configuration were left in place")
	}
}

var (
	settingsRe  = regexp.MustCompile(`(?is)<Settings>(.*?)</Settings>`)
	enabledRe   = regexp.MustCompile(`(?i)<Enabled>\s*(true|false)\s*</Enabled>`)
	logonTypeRe = regexp.MustCompile(`(?i)<LogonType>\s*([A-Za-z0-9]+)\s*</LogonType>`)
	argumentsRe = regexp.MustCompile(`(?is)<Arguments>(.*?)</Arguments>`)
	keyArgRe    = regexp.MustCompile(`--dpapi-key-file\s+("(?:[^"\\]|\\.)*"|\S+)`)
)

// taskDef is what Status needs from the task XML.
type taskDef struct {
	Headless bool   // principal logon type S4U
	KeyFile  string // the --dpapi-key-file argument
}

func xmlUnescape(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&").Replace(s)
}

func parseTask(doc string) taskDef {
	var d taskDef
	if mm := logonTypeRe.FindStringSubmatch(doc); mm != nil {
		d.Headless = strings.EqualFold(mm[1], "S4U")
	}
	if mm := argumentsRe.FindStringSubmatch(doc); mm != nil {
		if k := keyArgRe.FindStringSubmatch(xmlUnescape(mm[1])); k != nil {
			v := k[1]
			if strings.HasPrefix(v, `"`) {
				v = strings.ReplaceAll(v[1:len(v)-1], `\"`, `"`)
			}
			d.KeyFile = v
		}
	}
	return d
}

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
	doc := decodeConsole(out.Stdout)
	def := parseTask(doc)
	st.Headless = triOf(def.Headless)
	if def.Headless {
		st.KeyFile = def.KeyFile
		m.auditHeadless(&st)
	}
	if sec := settingsRe.FindStringSubmatch(doc); sec != nil {
		// Only the task-wide Enabled element lives directly under Settings.
		if mm := enabledRe.FindStringSubmatch(sec[1]); mm != nil && strings.EqualFold(mm[1], "false") {
			st.Enabled = No
		}
	}
	// Task Scheduler's own run state is only available as localized text, so
	// the caller decides from the engine's lock and heartbeat instead.
	st.Running = Unknown
	st.Details = append(st.Details, fmt.Sprintf("scheduled task %s is registered", TaskName))
	if def.Headless {
		st.Details = append(st.Details, "headless: runs whether or not anyone is signed in, and starts at boot")
	}
	return st, nil
}

// auditHeadless fills in the state of the DPAPI key file a headless task needs.
func (m *schtasks) auditHeadless(st *Status) {
	if st.KeyFile == "" {
		st.KeyFileMissing = true
		st.Details = append(st.Details, "the task is headless but names no key file ("+vault.DPAPIKeyFileName+"); install again with --headless")
		return
	}
	a := m.auditKey(st.KeyFile)
	st.KeyFileMissing, st.KeyFileErr, st.KeyFileBroad = a.Missing, a.ReadErr, a.Broad
	st.KeyFileACLErr, st.KeyFileDir, st.KeyFileDirBroad = a.ACLErr, a.Dir, a.DirBroad
	switch {
	case a.Missing:
		st.Details = append(st.Details, "the headless key file is missing; install again with --headless")
	case a.ReadErr != "":
		st.Details = append(st.Details, "the headless key file cannot be read: "+a.ReadErr)
	default:
		st.Details = append(st.Details, "headless key file: present, readable")
	}
	if len(a.Broad) > 0 {
		st.Details = append(st.Details, "WARNING: the key file's access list also admits "+strings.Join(a.Broad, ", ")+
			"; install again with --headless to reset it")
	}
	if len(a.DirBroad) > 0 {
		st.Details = append(st.Details, "WARNING: "+a.Dir+" can be changed by "+strings.Join(a.DirBroad, ", ")+
			", who could swap the key file; restrict that folder to you, SYSTEM and Administrators")
	}
	if a.ACLErr != "" {
		st.Details = append(st.Details, "could not read the key file's access list: "+a.ACLErr)
	}
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
