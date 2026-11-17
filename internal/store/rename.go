package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
)

// maxRenamedFrom is how many earlier names of an account are remembered.
const maxRenamedFrom = 8

// Rename is one account renamed from From to To.
type Rename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// accountTenantKey is the tenant key of an account that names no tenant of its
// own (config.Account.TenantKey).
func accountTenantKey(name string) string { return "account:" + name }

// ApplyRename moves everything the state keeps under the account name from to
// the name to: the account's state, the tenant keys derived from the name and
// the record of earlier names. It never touches the call log. A rename is a
// fact about the configuration, so it replaces whatever the state holds under
// to. When the state has nothing under from, the account's state may be in
// detached (what an engine set aside for accounts its config did not have,
// by name); it is taken from there.
func (s *State) ApplyRename(from, to string, detached map[string]*AccountState) {
	if from == to {
		return
	}
	if s.Accounts == nil {
		s.Accounts = map[string]*AccountState{}
	}
	if as, ok := s.Accounts[from]; ok {
		s.Accounts[to] = as
		delete(s.Accounts, from)
	} else if as, ok := detached[from]; ok {
		s.Accounts[to] = as
		delete(detached, from)
	} else {
		delete(s.Accounts, to) // the account had no state; do not leave another one's under its new name
	}
	fk, tk := accountTenantKey(from), accountTenantKey(to)
	if ts, ok := s.Tenants[fk]; ok {
		if s.Tenants[tk] == nil {
			s.Tenants[tk] = ts
		}
		delete(s.Tenants, fk)
	}
	if g, ok := s.TenantAliases[fk]; ok {
		if _, taken := s.TenantAliases[tk]; !taken {
			s.TenantAliases[tk] = g
		}
		delete(s.TenantAliases, fk)
	}
	for k, v := range s.TenantAliases {
		if v == fk {
			s.TenantAliases[k] = tk
		}
	}
	// The names the account had, then the one it just left.
	names := append(slices.Clone(s.RenamedFrom[from]), from)
	delete(s.RenamedFrom, from)
	names = append(slices.Clone(s.RenamedFrom[to]), names...)
	var out []string
	for _, n := range names {
		if n != to && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) > maxRenamedFrom {
		out = out[len(out)-maxRenamedFrom:]
	}
	if s.RenamedFrom == nil {
		s.RenamedFrom = map[string][]string{}
	}
	s.RenamedFrom[to] = out
}

// TidyRenames forgets the earlier names of accounts that are gone, and a
// name that a configured account uses now (so its log is not mixed up with
// the log of another account).
func (s *State) TidyRenames(configured func(string) bool) {
	for acct, olds := range s.RenamedFrom {
		if !configured(acct) {
			delete(s.RenamedFrom, acct)
			continue
		}
		var keep []string
		for _, n := range olds {
			if !configured(n) {
				keep = append(keep, n)
			}
		}
		if len(keep) == 0 {
			delete(s.RenamedFrom, acct)
		} else {
			s.RenamedFrom[acct] = keep
		}
	}
	if len(s.RenamedFrom) == 0 {
		s.RenamedFrom = nil
	}
}

// ApplyRenames applies renames to the state in the order they were made, so
// chains (a to b, then b to c) and swaps (a to tmp, b to a, tmp to b) come out
// right. They are always applied as a whole: a pending rename is only written
// after the configuration that holds it was saved, so an unclaimed one is
// never older than a configuration that was loaded before it. It returns the
// renames it applied.
func (s *State) ApplyRenames(rs []Rename, detached map[string]*AccountState) []Rename {
	var applied []Rename
	for _, r := range rs {
		if r.From == r.To {
			continue
		}
		s.ApplyRename(r.From, r.To, detached)
		applied = append(applied, r)
	}
	return applied
}

// PendingRenameFile is the name of the file a rename leaves in the data
// directory when an engine is running. Like PendingImportFile it is how a
// process that must not write state.json hands a change to the engine, which
// applies it when it loads the configuration that holds the new name.
const PendingRenameFile = "state.rename.json"

const renamingPrefix = "state.rename.adopting-"

// PendingRename is the content of PendingRenameFile.
type PendingRename struct {
	// Written is when the first of the renames was written; the engine drops them if they wait too long.
	Written time.Time `json:"written"`
	// Renames are in the order they were made.
	Renames []Rename `json:"renames"`
}

// PendingRenamePath returns where the pending renames of the data directory dir are.
func PendingRenamePath(dir string) string { return filepath.Join(dir, PendingRenameFile) }

// SavePendingRenames appends rs, which are newer, to the pending renames,
// atomically (mode 0600). written is the time of the first writing; the zero
// value means now.
func SavePendingRenames(dir string, written time.Time, rs []Rename) error {
	return savePendingRenames(dir, written, rs, false)
}

// RestorePendingRenames puts rs, which are older than whatever is waiting,
// back in front of it (an engine that claimed them could not use them yet).
func RestorePendingRenames(dir string, written time.Time, rs []Rename) error {
	return savePendingRenames(dir, written, rs, true)
}

func savePendingRenames(dir string, written time.Time, rs []Rename, older bool) error {
	p := &PendingRename{Written: written}
	if old, err := ReadPendingRenameFile(PendingRenamePath(dir)); err == nil && old != nil {
		if older {
			p.Renames = append(slices.Clone(rs), old.Renames...)
		} else {
			p.Renames = append(slices.Clone(old.Renames), rs...)
		}
		if !old.Written.IsZero() && (p.Written.IsZero() || old.Written.Before(p.Written)) {
			p.Written = old.Written
		}
	} else {
		p.Renames = slices.Clone(rs)
	}
	if p.Written.IsZero() {
		p.Written = time.Now()
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(PendingRenamePath(dir), append(b, '\n'))
}

// ReadPendingRenameFile reads a pending-rename file (also a claimed one); nil when there is none.
func ReadPendingRenameFile(path string) (*PendingRename, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p PendingRename
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s is damaged", PendingRenameFile)
	}
	if p.Written.IsZero() {
		if fi, err := os.Stat(path); err == nil {
			p.Written = fi.ModTime()
		}
	}
	return &p, nil
}

// ClaimPendingRenames takes the pending renames for the caller by renaming the
// file to a name of its own, like ClaimPendingImports; it also returns files an
// interrupted claim left behind. The caller deletes each when it is done.
func ClaimPendingRenames(dir string) ([]string, error) {
	var out []string
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	for _, de := range ents {
		if !de.IsDir() && strings.HasPrefix(de.Name(), renamingPrefix) {
			out = append(out, filepath.Join(dir, de.Name()))
		}
	}
	aside := filepath.Join(dir, fmt.Sprintf("%s%d-%d", renamingPrefix, os.Getpid(), time.Now().UnixNano()))
	switch err := os.Rename(PendingRenamePath(dir), aside); {
	case err == nil:
		out = append(out, aside)
	case !errors.Is(err, fs.ErrNotExist):
		return out, err
	}
	return out, nil
}

// ReadClaimedRenames reads the claimed files in order and returns their
// renames and the oldest time of writing. A damaged file is
// skipped and its error returned.
func ReadClaimedRenames(files []string) (rs []Rename, written time.Time, errs []error) {
	for _, f := range files {
		p, err := ReadPendingRenameFile(f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if p == nil {
			continue
		}
		rs = append(rs, p.Renames...)
		if written.IsZero() || p.Written.Before(written) {
			written = p.Written
		}
	}
	return rs, written, errs
}

// RemovePendingRename deletes the pending-rename file and any claimed one.
func RemovePendingRename(dir string) error {
	files := []string{PendingRenamePath(dir)}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, de := range ents {
			if strings.HasPrefix(de.Name(), renamingPrefix) {
				files = append(files, filepath.Join(dir, de.Name()))
			}
		}
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// HasPendingRename reports whether a pending-rename file exists.
func HasPendingRename(dir string) bool {
	_, err := os.Stat(PendingRenamePath(dir))
	return err == nil
}
