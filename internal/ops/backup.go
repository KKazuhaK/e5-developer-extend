package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/backup"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/engine"
	"github.com/KKazuhaK/e5-renew/internal/notify"
	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/store"
	"github.com/KKazuhaK/e5-renew/internal/vault"
	"github.com/KKazuhaK/e5-renew/internal/version"
)

// logNameRe matches the call log files a backup may carry.
var logNameRe = regexp.MustCompile(`^calls-\d{4}-\d{2}-\d{2}\.jsonl$`)

const guiFileName = "gui.json"

// ExportOptions are the choices of ExportBackup.
type ExportOptions struct {
	// WithLogs also exports the call records.
	WithLogs bool
	// Force overwrites an existing file.
	Force bool
}

// Codes of BackupError and of Note. The desktop app translates them; the
// command line shows the English text that goes with each.
const (
	// Errors.
	CodeFileExists          = "file_exists"           // the export target exists
	CodeTargetHasData       = "target_has_data"       // a default import into a machine that has data
	CodeMergeConflict       = "merge_conflict"        // Names: accounts present on both sides
	CodeLocalConfigBroken   = "local_config_broken"   // default or merge import while this machine's config is invalid
	CodeInvalidBackup       = "invalid_backup"        // the file decrypts but its content is not usable
	CodeNewerVersion        = "newer_version"         // the content comes from a newer e5renew
	CodeSaveFailed          = "save_failed"           // nothing was changed
	CodeRollbackFailed      = "rollback_failed"       // default and replace: Names are the vault entries that could not be put back
	CodeRollbackFailedMerge = "rollback_failed_merge" // merge: Names are the accounts that were being added

	// Notes.
	NoteStateDamaged     = "state_damaged"      // export: state.json left out
	NoteGUIDamaged       = "gui_damaged"        // export: gui.json left out
	NoteConfigSetAside   = "config_set_aside"   // import: Args[0] is the file name the broken config.toml was moved to
	NoteStatsNotImported = "stats_not_imported" // import: the state, interface settings or call records could not be written; Args name which (state, gui, logs)
	NoteStaleNotRemoved  = "stale_not_removed"  // import: Args are old vault entries that could not be removed
	SkippedMergeSettings = "merge_settings"     // import: merge keeps this machine's settings
	SkippedMergeLogs     = "merge_logs"         // import: merge does not import call records
)

// Note is a remark about an export or import that the desktop app can translate.
// The same remark is in the summary's English lists for the command line.
type Note struct {
	Code string   `json:"code"`
	Args []string `json:"args"`
}

// BackupError is a refusal or failure the desktop app can tell apart and
// translate. Its Error text is the English message of the command line.
type BackupError struct {
	Code  string
	Names []string
	Msg   string
	Err   error
}

func (e *BackupError) Error() string { return e.Msg }
func (e *BackupError) Unwrap() error { return e.Err }

// BackupSummary says what an export wrote. It names accounts and counts
// things; it never holds a secret, a token or the webhook address.
type BackupSummary struct {
	Path         string   `json:"path"`
	Accounts     []string `json:"accounts"`
	VaultEntries int      `json:"vaultEntries"`
	IncludesLogs bool     `json:"includesLogs"`
	LogFiles     int      `json:"logFiles"`
	Notes        []string `json:"notes"`
	NoteCodes    []Note   `json:"noteCodes"`
}

// ImportMode says how an import treats what the machine already has.
type ImportMode string

const (
	// ImportDefault only imports into a machine that has no accounts and no vault data.
	ImportDefault ImportMode = "default"
	// ImportMerge adds the accounts the machine does not have.
	ImportMerge ImportMode = "merge"
	// ImportReplace replaces everything except the machine's [vault] setting.
	ImportReplace ImportMode = "replace"
)

// ImportSummary says what an import did.
type ImportSummary struct {
	Mode ImportMode `json:"mode"`
	// Accounts are the accounts that came from the backup.
	Accounts      []string `json:"accounts"`
	VaultEntries  int      `json:"vaultEntries"`
	SourceOS      string   `json:"sourceOS"`
	SourceVersion string   `json:"sourceVersion"`
	Created       string   `json:"created"`
	// NewMasterKey is true when this machine had no master key and one was created.
	NewMasterKey bool `json:"newMasterKey"`
	// Skipped lists what was left out and why.
	Skipped []string `json:"skipped"`
	Notes   []string `json:"notes"`
	// SkippedCodes and NoteCodes say the same as Skipped and Notes, as codes.
	SkippedCodes []Note `json:"skippedCodes"`
	NoteCodes    []Note `json:"noteCodes"`
	// Hint is the next step after an import.
	Hint string `json:"hint"`
}

// CheckBackupTarget fails when path exists and force is not set, so a caller
// can refuse before it asks for a passphrase.
func CheckBackupTarget(path string, force bool) error {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("backup file: %w", err)
	case fi.IsDir():
		return fmt.Errorf("%s is a directory; give a file name", path)
	case !force:
		return &BackupError{Code: CodeFileExists, Msg: path + " already exists; use --force to overwrite it"}
	}
	return nil
}

// BackupTargetHasData reports whether this machine already has accounts or
// vault data, which decides whether an import needs --merge or --replace.
func (o *Ops) BackupTargetHasData() (bool, error) {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return false, err
	}
	if len(cfg.Accounts) > 0 {
		return true, nil
	}
	v, err := o.OpenVault(cfg)
	if err != nil {
		return false, err
	}
	keys, err := v.List()
	return len(keys) > 0, err
}

// ExportBackup writes the whole installation to one file, encrypted with passphrase.
func (o *Ops) ExportBackup(ctx context.Context, path, passphrase string, opts ExportOptions) (BackupSummary, error) {
	if err := backup.CheckPassphrase(passphrase); err != nil {
		return BackupSummary{}, err
	}
	if err := CheckBackupTarget(path, opts.Force); err != nil {
		return BackupSummary{}, err
	}
	payload, sum, err := o.collectBackup(ctx, opts)
	if err != nil {
		return BackupSummary{}, err
	}
	data, err := backup.Seal(payload, passphrase) // fails with ErrPayloadTooLarge when import could not read it back
	if err != nil {
		return BackupSummary{}, err
	}
	if len(data) > backup.MaxFileSize {
		return BackupSummary{}, fmt.Errorf("the backup would be %d MiB, over the %d MiB limit; leave out the call records",
			len(data)>>20, backup.MaxFileSize>>20)
	}
	if err := paths.WriteFileAtomicInPlace(path, data); err != nil {
		return BackupSummary{}, fmt.Errorf("write backup: %w", err)
	}
	sum.Path = path
	return sum, nil
}

func (s *BackupSummary) addNote(code, text string, args ...string) {
	s.Notes = append(s.Notes, text)
	s.NoteCodes = append(s.NoteCodes, Note{Code: code, Args: args})
}

func (s *ImportSummary) addNote(code, text string, args ...string) {
	s.Notes = append(s.Notes, text)
	s.NoteCodes = append(s.NoteCodes, Note{Code: code, Args: args})
}

func (s *ImportSummary) addSkipped(code, text string) {
	s.Skipped = append(s.Skipped, text)
	s.SkippedCodes = append(s.SkippedCodes, Note{Code: code})
}

// collectBackup reads everything a backup holds, under the config lock so it
// sees one consistent configuration.
func (o *Ops) collectBackup(ctx context.Context, opts ExportOptions) (*backup.Payload, BackupSummary, error) {
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return nil, BackupSummary{}, err
	}
	defer unlock()

	cfg, cfgPath, err := o.LoadConfig()
	if err != nil {
		return nil, BackupSummary{}, err
	}
	cfgText, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, BackupSummary{}, fmt.Errorf("read config: %w", err)
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return nil, BackupSummary{}, err
	}
	cfgDir, err := paths.ConfigDir()
	if err != nil {
		return nil, BackupSummary{}, err
	}

	p := &backup.Payload{
		Created:    time.Now().Format(time.RFC3339),
		AppVersion: version.Version,
		SourceOS:   runtime.GOOS,
		Config:     string(cfgText),
		Vault:      map[string][]byte{},
	}
	var sum BackupSummary
	for _, a := range cfg.Accounts {
		sum.Accounts = append(sum.Accounts, a.Name)
	}

	v, err := o.OpenVault(cfg)
	if err != nil {
		return nil, sum, err
	}
	keys, err := v.List()
	if err != nil {
		return nil, sum, err
	}
	var bad []string
	var firstErr error
	for _, k := range keys {
		val, err := v.Get(k)
		if err != nil {
			bad = append(bad, k)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		p.Vault[k] = val
	}
	if len(bad) > 0 {
		// Nothing is skipped: a backup that silently lacks a secret would only be found out on the day it is needed.
		return nil, sum, WithHint(fmt.Errorf("cannot read %d of %d vault entries (%s), so nothing was exported: %w",
			len(bad), len(keys), strings.Join(bad, ", "), firstErr), "<account>")
	}
	sum.VaultEntries = len(p.Vault)

	if p.State, err = readJSONFile(filepath.Join(dataDir, "state.json")); err != nil {
		sum.addNote(NoteStateDamaged, "state.json is damaged and was left out")
	}
	if p.GUI, err = readJSONFile(filepath.Join(cfgDir, guiFileName)); err != nil {
		sum.addNote(NoteGUIDamaged, guiFileName+" is damaged and was left out")
	}
	if opts.WithLogs {
		if p.Logs, err = readLogFiles(filepath.Join(dataDir, "logs")); err != nil {
			return nil, sum, err
		}
		sum.IncludesLogs, sum.LogFiles = true, len(p.Logs)
	}
	return p, sum, nil
}

// readJSONFile returns the content of a JSON file, nil when it does not exist.
// A file that is not valid JSON is reported as an error and not returned.
func readJSONFile(path string) (json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, errors.New("not valid JSON")
	}
	return b, nil
}

func readLogFiles(dir string) (map[string]string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read call records: %w", err)
	}
	out := map[string]string{}
	for _, de := range ents {
		if de.IsDir() || !logNameRe.MatchString(de.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			return nil, fmt.Errorf("read call records: %w", err)
		}
		out[de.Name()] = string(b)
	}
	return out, nil
}

// importPlan is a backup that has been decrypted and fully checked.
type importPlan struct {
	p     *backup.Payload
	cfg   *config.Config
	state *store.State
	gui   []byte
}

// ImportBackup restores a backup file. Everything is decrypted and checked
// before the first byte is written.
func (o *Ops) ImportBackup(ctx context.Context, path, passphrase string, mode ImportMode) (ImportSummary, error) {
	switch mode {
	case "":
		mode = ImportDefault
	case ImportDefault, ImportMerge, ImportReplace:
	default:
		return ImportSummary{}, fmt.Errorf("unknown import mode %q", mode)
	}
	data, err := readBackupFile(path)
	if err != nil {
		return ImportSummary{}, err
	}
	p, err := backup.Open(data, passphrase)
	if err != nil {
		return ImportSummary{}, err
	}
	plan, err := checkBackup(p, o.localVaultSetting())
	if err != nil {
		return ImportSummary{}, err
	}

	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return ImportSummary{}, err
	}
	sum, err := o.applyImport(ctx, plan, mode)
	unlock()
	if err != nil {
		return ImportSummary{}, err
	}
	// After the unlock: the engine takes the config lock itself when it reloads.
	o.ReloadEngine()
	return sum, nil
}

// localVaultSetting is this machine's [vault] setting, read leniently: a
// config that is missing or invalid elsewhere still gives its [vault] section,
// and anything unreadable gives the default.
func (o *Ops) localVaultSetting() config.Vault {
	if path, err := paths.ConfigFile(); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			if v, ok := config.VaultSetting(data); ok {
				return v
			}
		}
	}
	return config.Default().Vault
}

func readBackupFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open backup: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, backup.MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read backup: %w", err)
	}
	if len(data) > backup.MaxFileSize {
		return nil, backup.ErrTooLarge
	}
	return data, nil
}

func invalidBackup(msg string, cause error) error {
	e := &BackupError{Code: CodeInvalidBackup, Msg: msg, Err: cause}
	if cause != nil {
		e.Msg = msg + ": " + cause.Error()
	}
	return e
}

// checkBackup validates the content of a backup. The backup's own [vault]
// setting is never imported, so machineVault stands in for it before the
// config is validated: a backend this machine cannot use does not matter.
func checkBackup(p *backup.Payload, machineVault config.Vault) (*importPlan, error) {
	cfg, err := config.ParseWithVault([]byte(p.Config), machineVault)
	if err != nil {
		return nil, invalidBackup("the backup holds an invalid config", err)
	}
	plan := &importPlan{p: p, cfg: cfg}

	if p.State != nil {
		var st store.State
		if err := json.Unmarshal(p.State, &st); err != nil {
			return nil, invalidBackup("the backup holds an invalid state.json", nil)
		}
		if st.Version > store.StateVersion {
			return nil, &BackupError{Code: CodeNewerVersion, Msg: "the backup's state.json comes from a newer version of e5renew; upgrade e5renew first"}
		}
		if st.Accounts == nil {
			st.Accounts = map[string]*store.AccountState{}
		}
		for k, a := range st.Accounts {
			if a == nil {
				delete(st.Accounts, k)
			}
		}
		plan.state = &st
	}
	if p.GUI != nil {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(p.GUI, &obj); err != nil {
			return nil, invalidBackup("the backup holds an invalid "+guiFileName, nil)
		}
		plan.gui = p.GUI
	}

	for k := range p.Vault {
		if !knownVaultKey(cfg, k) {
			return nil, invalidBackup("the backup holds a vault entry that this version does not know: "+k, nil)
		}
	}
	for name := range p.Logs {
		if !logNameRe.MatchString(name) {
			return nil, invalidBackup("the backup holds a call record file with an unexpected name", nil)
		}
	}
	return plan, nil
}

// knownVaultKey accepts only the entries e5renew itself writes.
func knownVaultKey(cfg *config.Config, key string) bool {
	if key == notify.VaultKey {
		return true
	}
	for _, a := range cfg.Accounts {
		if key == vault.SecretKey(a.Name) || key == vault.TokenCacheKey(a.Name) {
			return true
		}
	}
	return false
}

// lockVaultKeys takes the lock of every token cache among keys, in sorted
// order, and returns the function that releases them. The engine takes the
// same lock while it refreshes a token, so holding them from the snapshot
// through the writes and a rollback keeps a refresh from interleaving.
//
// Lock order, to stay clear of deadlocks: the config lock (taken by
// ImportBackup), then these vault key locks, then a non-waiting try on the
// engine lock. The engine never asks for the config lock while it holds a key
// lock, and nothing waits on the engine lock.
func lockVaultKeys(ctx context.Context, v *vault.Vault, keys []string) (func(), error) {
	var names []string
	for _, k := range keys {
		if strings.HasPrefix(k, "tokencache/") {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	var unlocks []func()
	release := func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
	for _, k := range names {
		unlock, err := v.Lock(ctx, k)
		if err != nil {
			release()
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	return release, nil
}

// vaultUndo remembers the stored form of vault entries so a failed import can put them back.
type vaultUndo struct {
	v    *vault.Vault
	keys []string
	raw  map[string][]byte
	had  map[string]bool
}

// snapshotVault reads the stored bytes of keys. It needs no master key, so an
// entry this machine cannot decrypt is kept and restored as it was.
func snapshotVault(v *vault.Vault, keys []string) (*vaultUndo, error) {
	u := &vaultUndo{v: v, keys: keys, raw: map[string][]byte{}, had: map[string]bool{}}
	for _, k := range keys {
		raw, had, err := v.Snapshot(k)
		if err != nil {
			return nil, err
		}
		u.raw[k], u.had[k] = raw, had
	}
	return u, nil
}

// restore puts every entry back and returns the ones it could not.
func (u *vaultUndo) restore() (failed []string) {
	for _, k := range u.keys {
		if err := u.v.Restore(k, u.raw[k], u.had[k]); err != nil {
			failed = append(failed, k)
		}
	}
	return failed
}

// abort undoes the vault changes after cause and says whether that worked.
// The caller holds the locks of the token caches. accounts are the accounts a
// merge was adding.
func (u *vaultUndo) abort(cause error, mode ImportMode, accounts []string) error {
	if failed := u.restore(); len(failed) > 0 {
		return rollbackFailed(cause, mode, failed, accounts)
	}
	return &BackupError{Code: CodeSaveFailed, Err: cause, Msg: fmt.Sprintf("%v\nnothing was changed", cause)}
}

// rollbackFailed words what to do after the old vault entries could not be put
// back. A replace or default import can simply be run again; a merge has
// possibly left entries of the added accounts, which must go first.
func rollbackFailed(cause error, mode ImportMode, failed, accounts []string) error {
	if mode == ImportMerge {
		return &BackupError{
			Code: CodeRollbackFailedMerge, Names: accounts, Err: cause,
			Msg: fmt.Sprintf("%v\nthe old vault entries %s could not be put back, and vault entries of the accounts being added (%s) may be left behind;\n"+
				"remove them (e5renew account remove NAME) and merge again", cause, strings.Join(failed, ", "), strings.Join(accounts, ", ")),
		}
	}
	return &BackupError{
		Code: CodeRollbackFailed, Names: failed, Err: cause,
		Msg: fmt.Sprintf("%v\nthe old vault entries %s could not be put back; run `e5renew doctor`, and if accounts fail, import the same backup again with --replace",
			cause, strings.Join(failed, ", ")),
	}
}

// targetConfig loads this machine's config for an import. A config that exists
// but is invalid blocks a default or merge import; a replace sets it aside (the
// caller does) and starts from the defaults, keeping only the [vault] section
// when that can still be read. broken is true in that case.
func (o *Ops) targetConfig(mode ImportMode) (cfg *config.Config, path string, broken bool, err error) {
	cfg, path, err = o.LoadConfigOrDefault()
	if err == nil {
		return cfg, path, false, nil
	}
	data, rerr := os.ReadFile(path)
	if path == "" || rerr != nil {
		return nil, path, false, err // not a broken file: it cannot be read at all
	}
	if mode != ImportReplace {
		return nil, path, false, &BackupError{
			Code: CodeLocalConfigBroken, Err: err,
			Msg: fmt.Sprintf("this machine's config.toml is invalid, so the backup cannot be added to it:\n%v\n"+
				"fix it, or use --replace to set it aside and restore the backup in its place", err),
		}
	}
	cfg = config.Default()
	if v, ok := config.VaultSetting(data); ok {
		cfg.Vault = v
	}
	return cfg, path, true, nil
}

// applyImport writes the plan. The caller holds the config lock.
//
// Order: the vault entries are written first (their old stored form is kept
// in memory), then config.toml. If the config cannot be saved the vault is put
// back and nothing has changed. Only once the config is saved are the entries
// of a replace that the backup lacks removed, and the state and other files written.
func (o *Ops) applyImport(ctx context.Context, plan *importPlan, mode ImportMode) (ImportSummary, error) {
	p := plan.p
	sum := ImportSummary{
		Mode: mode, SourceOS: p.SourceOS, SourceVersion: p.AppVersion, Created: p.Created,
		Hint: "accounts whose tenant ties tokens to the device (Conditional Access) may ask you to sign in again; run `e5renew doctor` to check",
	}

	tcfg, cfgPath, broken, err := o.targetConfig(mode)
	if err != nil {
		return sum, err
	}
	v, err := o.OpenVault(tcfg)
	if err != nil {
		return sum, err
	}
	existing, err := v.List()
	if err != nil {
		return sum, err
	}
	targetHasData := len(tcfg.Accounts) > 0 || len(existing) > 0

	var final *config.Config
	// puts are the vault entries to write, newAccounts the accounts coming from the backup.
	puts := map[string][]byte{}
	var newAccounts []string
	switch mode {
	case ImportMerge:
		var conflicts []string
		for _, a := range plan.cfg.Accounts {
			if _, ok := tcfg.Find(a.Name); ok {
				conflicts = append(conflicts, a.Name)
			}
		}
		if len(conflicts) > 0 {
			return sum, &BackupError{
				Code: CodeMergeConflict, Names: conflicts,
				Msg: fmt.Sprintf("these accounts exist on this machine and in the backup: %s\n"+
					"nothing was changed; remove them here first, or use --replace", strings.Join(conflicts, ", ")),
			}
		}
		final = tcfg
		for _, a := range plan.cfg.Accounts {
			final.Accounts = append(final.Accounts, a)
			newAccounts = append(newAccounts, a.Name)
			for _, k := range []string{vault.SecretKey(a.Name), vault.TokenCacheKey(a.Name)} {
				if val, ok := p.Vault[k]; ok {
					puts[k] = val
				}
			}
		}
		if err := final.Validate(); err != nil {
			return sum, invalidBackup("the merged config is invalid", err)
		}
		sum.addSkipped(SkippedMergeSettings, "schedule, alert settings, webhook and interface settings (merge keeps this machine's)")
		if len(p.Logs) > 0 {
			sum.addSkipped(SkippedMergeLogs, "call records (merge does not import them)")
		}
	default:
		if mode == ImportDefault && targetHasData {
			return sum, &BackupError{
				Code: CodeTargetHasData,
				Msg: "this machine already has accounts or vault data\n" +
					"to add the backup's accounts next to them use --merge; to replace everything use --replace",
			}
		}
		final = plan.cfg
		// [vault] is a setting of this machine, not part of what moves.
		final.Vault = tcfg.Vault
		for _, a := range final.Accounts {
			newAccounts = append(newAccounts, a.Name)
		}
		for k, val := range p.Vault {
			puts[k] = val
		}
	}
	sum.Accounts, sum.VaultEntries = newAccounts, len(puts)

	keys := make([]string, 0, len(puts))
	for k := range puts {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	// stale are the old entries a replace drops once the new config is in place.
	var stale []string
	if mode == ImportReplace {
		for _, k := range existing {
			if _, keep := puts[k]; !keep {
				stale = append(stale, k)
			}
		}
	}

	var created bool
	if len(puts) > 0 && o.VaultFor == nil {
		if created, err = ensureMasterKey(final.Vault.Backend); err != nil {
			return sum, err
		}
	}
	sum.NewMasterKey = created

	affected := append(slices.Clone(keys), stale...)
	unlockKeys, err := lockVaultKeys(ctx, v, affected)
	if err != nil {
		return sum, &BackupError{Code: CodeSaveFailed, Err: err, Msg: fmt.Sprintf("%v\nnothing was changed", err)}
	}
	defer unlockKeys() // after the config is saved or the vault is back; before ImportBackup reloads the engine
	undo, err := snapshotVault(v, affected)
	if err != nil {
		return sum, &BackupError{Code: CodeSaveFailed, Err: err, Msg: fmt.Sprintf("read the old vault entries: %v\nnothing was changed", err)}
	}
	for _, k := range keys {
		if err := v.Put(k, puts[k]); err != nil {
			return sum, undo.abort(WithHint(fmt.Errorf("write vault entry %s: %w", k, err), accountOfKey(k)), mode, newAccounts)
		}
	}

	// The state goes first to a running engine, before the config is saved, so
	// a reload that sees the new accounts finds their state too.
	st, err := prepareState(plan, mode, newAccounts)
	if err != nil {
		return sum, undo.abort(err, mode, newAccounts)
	}
	defer st.close()

	var asideName string
	if broken {
		aside, err := setAside(cfgPath)
		if err != nil {
			st.discard()
			return sum, undo.abort(err, mode, newAccounts)
		}
		asideName = aside
	}
	save := o.SaveConfig
	if save == nil {
		save = func(c *config.Config, path string) error { return c.Save(path) }
	}
	if err := save(final, cfgPath); err != nil {
		if asideName != "" {
			_ = os.Remove(filepath.Join(filepath.Dir(cfgPath), asideName))
		}
		st.discard()
		return sum, undo.abort(fmt.Errorf("save config.toml: %w", err), mode, newAccounts)
	}
	// From here on the import has succeeded: what fails below is reported as a note.
	if asideName != "" {
		sum.addNote(NoteConfigSetAside, "this machine's config.toml was invalid; it was kept as "+asideName, asideName)
	}

	var notRemoved []string
	for _, k := range stale {
		if err := v.Delete(k); err != nil && !errors.Is(err, vault.ErrNotFound) {
			notRemoved = append(notRemoved, k)
		}
	}
	if len(notRemoved) > 0 {
		sum.addNote(NoteStaleNotRemoved, "old vault entries that could not be removed (they are not used any more): "+strings.Join(notRemoved, ", "), notRemoved...)
	}

	if failed, cause := o.writeImportedFiles(plan, mode, st); len(failed) > 0 {
		sum.addNote(NoteStatsNotImported, fmt.Sprintf("the accounts and secrets were imported, but statistics were not (%s: %v)", strings.Join(failed, ", "), cause), failed...)
	}
	return sum, nil
}

// setAside copies the invalid config.toml next to itself and returns the new file name.
func setAside(cfgPath string) (string, error) {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", fmt.Errorf("keep the invalid config.toml: %w", err)
	}
	name := filepath.Base(cfgPath) + ".broken-" + time.Now().Format("20060102-150405")
	if err := paths.WriteFileAtomic(filepath.Join(filepath.Dir(cfgPath), name), data); err != nil {
		return "", fmt.Errorf("keep the invalid config.toml: %w", err)
	}
	return name, nil
}

// engineProbeWait is how long an import keeps trying to take the engine lock
// before it concludes an engine is running: `status` and other probes hold it
// for a moment.
var engineProbeWait = 1500 * time.Millisecond

// acquireEngineLock takes the engine lock without keeping the engine out for
// long: it retries for engineProbeWait and returns engine.ErrRunning only if
// the lock stayed taken.
func acquireEngineLock(dataDir string) (func(), error) {
	deadline := time.Now().Add(engineProbeWait)
	for {
		release, err := engine.AcquireLock(dataDir)
		if !errors.Is(err, engine.ErrRunning) || !time.Now().Before(deadline) {
			return release, err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// stateHandover gives the imported per-account state to this machine.
//
// A running engine keeps the state in memory and saves it over state.json on
// every reload, so writing state.json under it would be undone. While it runs
// the state goes to state.import.json instead, written before config.toml, and
// the engine adopts it (engine.adoptPending). Without an engine, state.json is
// written directly, with the engine lock held from here until the end so none
// starts halfway.
type stateHandover struct {
	dataDir string
	// direct is the whole state to write to state.json (default and replace imports without an engine),
	// merge the entries to add to it (merge without an engine); both are nil when an engine has the state.
	direct  *store.State
	merge   map[string]*store.AccountState
	release func() // the engine lock, when held
	handed  bool   // the state was handed to a running engine
}

func (h *stateHandover) close() {
	if h.release != nil {
		h.release()
		h.release = nil
	}
}

// discard takes back a handover after the config could not be saved.
func (h *stateHandover) discard() {
	if h.handed {
		_ = store.RemovePendingImport(h.dataDir)
		h.handed = false
	}
}

func prepareState(plan *importPlan, mode ImportMode, newAccounts []string) (*stateHandover, error) {
	dataDir, err := paths.DataDir()
	if err != nil {
		return nil, err
	}
	h := &stateHandover{dataDir: dataDir}
	if plan.state == nil {
		return h, nil
	}
	imported := plan.state.Accounts
	pending := &store.PendingImport{Mode: store.PendingReplace, Accounts: imported}
	if mode == ImportMerge {
		pending.Mode, pending.Accounts = store.PendingMerge, map[string]*store.AccountState{}
		for _, name := range newAccounts {
			if as, ok := imported[name]; ok {
				pending.Accounts[name] = as
			}
		}
		if len(pending.Accounts) == 0 {
			return h, nil
		}
		h.merge = pending.Accounts
	} else {
		h.direct = plan.state
	}

	release, err := acquireEngineLock(dataDir)
	switch {
	case errors.Is(err, engine.ErrRunning):
		h.direct, h.merge = nil, nil
		if err := store.SavePendingImport(dataDir, pending); err != nil {
			return nil, fmt.Errorf("hand the state to the running engine: %w", err)
		}
		h.handed = true
	case err != nil:
		return nil, err
	default:
		h.release = release
	}
	return h, nil
}

// writeImportedFiles writes what is left after the config is saved: the state
// (unless it went to the engine), gui.json and the call records. It returns
// which parts failed ("state", "gui", "logs") and the first error; none of
// them undoes the import.
func (o *Ops) writeImportedFiles(plan *importPlan, mode ImportMode, h *stateHandover) (failed []string, first error) {
	fail := func(part string, err error) {
		failed = append(failed, part)
		if first == nil {
			first = err
		}
	}
	dataDir := h.dataDir
	if h.direct != nil || h.merge != nil {
		// Left over from an import whose engine stopped before it looked: this write supersedes it.
		_ = store.RemovePendingImport(dataDir)
		st := store.New(dataDir)
		var err error
		if h.merge != nil {
			// A merge keeps the local statistics, so it must not write over a
			// state.json it could not read (newer version, permissions): the
			// existing accounts' history would be replaced by nothing.
			var cur *store.State
			if cur, err = st.LoadState(); err == nil {
				for name, as := range h.merge {
					cur.Accounts[name] = as
				}
				err = st.SaveState(cur)
			}
		} else {
			err = st.SaveState(h.direct)
		}
		if err != nil {
			fail("state", fmt.Errorf("write state.json: %w", err))
		}
	}
	h.close()
	if mode == ImportMerge {
		return
	}
	cfgDir, err := paths.ConfigDir()
	if err != nil {
		fail("gui", err)
		return
	}
	if plan.gui != nil {
		var buf bytes.Buffer
		err := json.Indent(&buf, plan.gui, "", "  ")
		if err == nil {
			buf.WriteByte('\n')
			err = paths.WriteFileAtomic(filepath.Join(cfgDir, guiFileName), buf.Bytes())
		}
		if err != nil {
			fail("gui", fmt.Errorf("write %s: %w", guiFileName, err))
		}
	}
	for name, text := range plan.p.Logs {
		if err := paths.WriteFileAtomic(filepath.Join(dataDir, "logs", name), []byte(text)); err != nil {
			fail("logs", fmt.Errorf("write call records: %w", err))
			break
		}
	}
	return
}

func accountOfKey(key string) string {
	_, name, _ := strings.Cut(key, "/")
	return name
}

// ensureMasterKey makes sure this machine has a master key to write with. A
// key that exists is used; with none, and an empty vault, one is created in
// the system keyring like `e5renew vault init` does. It reports whether it created one.
func ensureMasterKey(backend string) (bool, error) {
	_, _, err := vault.LookupMasterKey(backend)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, vault.ErrNoMasterKey) || errors.Is(err, vault.ErrRemoteSession) || backend == vault.BackendFile {
		return false, WithHint(err, "")
	}
	dir, derr := paths.VaultDir()
	if derr != nil {
		return false, derr
	}
	switch err := vault.InitKeyring(dir); {
	case err == nil:
		return true, nil
	case errors.Is(err, vault.ErrKeyExists):
		return false, nil
	default:
		return false, WithHint(err, "")
	}
}
