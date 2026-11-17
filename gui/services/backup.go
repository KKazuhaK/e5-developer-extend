package services

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/backup"
	"github.com/KKazuhaK/e5-renew/internal/ops"
)

// BackupService exports everything to one file and restores it (docs/user/backup.md).
type BackupService struct{ b *Backend }

// NewBackupService returns the service.
func NewBackupService(b *Backend) *BackupService { return &BackupService{b} }

// backupExt is the extension of backup files.
const backupExt = ".e5backup"

// defaultBackupName is the file name the save dialog starts with, for the day now.
func defaultBackupName(now time.Time) string {
	return "e5renew-backup-" + now.Format("20060102") + backupExt
}

// TargetHasData reports whether this machine already has accounts or vault
// data. When it does an import must be a merge or a replace. It fails when
// that cannot be found out (an invalid config.toml, say); a replace still works then.
func (s *BackupService) TargetHasData() (bool, error) { return s.b.Ops.BackupTargetHasData() }

// Export asks where to save the backup, then writes it encrypted with passphrase.
// dialogTitle is the (translated) text of the file dialog. The dialog confirms
// the overwrite of a file it was given. When the user cancels nothing is
// written and Cancelled is set. A refusal the user can act on comes back as
// Problem, with a code the interface translates.
func (s *BackupService) Export(passphrase string, withLogs bool, dialogTitle string) (BackupExportResult, error) {
	if s.b.PickSaveFile == nil {
		return BackupExportResult{}, errors.New("file dialogs are not available")
	}
	path, err := s.b.PickSaveFile(dialogTitle, defaultBackupName(time.Now()))
	if err != nil {
		return BackupExportResult{}, err
	}
	if path == "" {
		return BackupExportResult{Cancelled: true}, nil
	}
	// Overwriting is allowed for the exact path the dialog returned, which it asked the user about.
	// A path this code changed was never confirmed: it must not replace anything.
	force := true
	if !strings.Contains(baseName(path), ".") {
		path += backupExt
		force = false
	}
	defer s.b.begin()()
	sum, err := s.b.Ops.ExportBackup(context.Background(), path, passphrase, ops.ExportOptions{WithLogs: withLogs, Force: force})
	if err != nil {
		if p := problemOf(err); p != nil {
			return BackupExportResult{Problem: p}, nil
		}
		return BackupExportResult{}, err
	}
	return BackupExportResult{Summary: sum}, nil
}

// Import asks which backup to open, then restores it. mode is "default",
// "merge" or "replace". When the user cancels the dialog nothing is changed
// and Cancelled is set. A refusal the user can act on comes back as Problem.
func (s *BackupService) Import(passphrase, mode, dialogTitle string) (BackupImportResult, error) {
	var m ops.ImportMode
	switch ops.ImportMode(mode) {
	case ops.ImportDefault, ops.ImportMerge, ops.ImportReplace:
		m = ops.ImportMode(mode)
	default:
		return BackupImportResult{}, errors.New("mode must be default, merge or replace")
	}
	if s.b.PickOpenFile == nil {
		return BackupImportResult{}, errors.New("file dialogs are not available")
	}
	path, err := s.b.PickOpenFile(dialogTitle)
	if err != nil {
		return BackupImportResult{}, err
	}
	if path == "" {
		return BackupImportResult{Cancelled: true}, nil
	}
	defer s.b.begin()()
	sum, err := s.b.Ops.ImportBackup(context.Background(), path, passphrase, m)
	if err != nil {
		if p := problemOf(err); p != nil {
			return BackupImportResult{Problem: p}, nil
		}
		return BackupImportResult{}, err
	}
	return BackupImportResult{Summary: sum}, nil
}

// Problem codes of errors that come from the backup file itself; the others are the ops.Code* values.
const (
	problemNotBackup       = "not_backup"
	problemNewerVersion    = "newer_version"
	problemBadFile         = "bad_file"
	problemWrongPassphrase = "wrong_passphrase"
	problemTooLarge        = "too_large"
	problemPayloadTooLarge = "payload_too_large"
	problemShortPassphrase = "short_passphrase"
)

// problemOf turns an error the user can act on into a typed problem, nil for any other error.
func problemOf(err error) *BackupProblem {
	for sentinel, code := range map[error]string{
		backup.ErrNotBackup: problemNotBackup, backup.ErrTooNew: problemNewerVersion, backup.ErrBadFile: problemBadFile,
		backup.ErrDecrypt: problemWrongPassphrase, backup.ErrTooLarge: problemTooLarge,
		backup.ErrPayloadTooLarge: problemPayloadTooLarge, backup.ErrShortPassphrase: problemShortPassphrase,
	} {
		if errors.Is(err, sentinel) {
			return &BackupProblem{Code: code, Names: []string{}}
		}
	}
	var be *ops.BackupError
	if !errors.As(err, &be) {
		return nil
	}
	p := &BackupProblem{Code: be.Code, Names: append([]string{}, be.Names...)}
	switch {
	case be.Code == ops.CodeInvalidBackup:
		p.Detail = be.Msg
	case be.Err != nil:
		p.Detail = be.Err.Error()
	}
	return p
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
