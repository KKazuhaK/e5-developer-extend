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
	"sync/atomic"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
)

// maxRenamedFrom is how many earlier names of an account are remembered.
const maxRenamedFrom = 8

// Rename is one account renamed from From to To.
type Rename struct {
	From string `json:"from"`
	To   string `json:"to"`
	// ID is unique to the rename; a state remembers the IDs it applied.
	ID string `json:"id,omitempty"`
	// Uncommitted marks a rename written before its config.toml was saved (by a
	// process that holds the engine lock and applies it itself). Whoever finds
	// it later, after a crash, applies it only if the config has the new name
	// and no longer the old one.
	Uncommitted bool `json:"uncommitted,omitempty"`
}

// Valid reports whether the rename may be applied against a configuration
// for which has says whether an account of a name exists: a committed rename
// always may, an uncommitted one only when the configuration got as far as
// having the new name and not the old one.
func (r Rename) Valid(has func(string) bool) bool {
	return !r.Uncommitted || (has(r.To) && !has(r.From))
}

// accountTenantKey is the tenant key of an account that names no tenant of its
// own (config.Account.TenantKey).
func accountTenantKey(name string) string { return "account:" + name }

// maxRenameIDs is how many applied rename IDs a state remembers.
const maxRenameIDs = 64

// ApplyRenameOnce applies r unless this state has applied it already: by its
// ID, or, for a rename without one, because the account is already where r puts
// it (nothing under From, and To remembers From). It reports whether it applied it.
func (s *State) ApplyRenameOnce(r Rename, detached map[string]*AccountState) bool {
	if r.From == r.To {
		return false
	}
	if r.ID != "" && slices.Contains(s.RenameIDs, r.ID) {
		return false
	}
	_, live := s.Accounts[r.From]
	if !live && detached[r.From] == nil && slices.Contains(s.RenamedFrom[r.To], r.From) {
		return false
	}
	s.ApplyRename(r.From, r.To, detached)
	if r.ID != "" {
		s.RenameIDs = append(s.RenameIDs, r.ID)
		if len(s.RenameIDs) > maxRenameIDs {
			s.RenameIDs = s.RenameIDs[len(s.RenameIDs)-maxRenameIDs:]
		}
	}
	return true
}

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
		if s.ApplyRenameOnce(r, detached) {
			applied = append(applied, r)
		}
	}
	return applied
}

// Pending renames: a process that must not write state.json hands a rename to
// the running engine in a file of its own, state.rename.<seq>.json, where seq
// is a number that grows with every file. Each rename is a separate file that
// is written once and never changed, so a writer never races the engine's claim
// (the engine takes files, in seq order, by renaming them); a file written while
// the engine claims simply waits for the next claim.
const (
	pendingRenamePrefix = "state.rename."
	claimedRenamePrefix = "state.rename.adopting-"
	pendingRenameSuffix = ".json"
)

// PendingRename is the content of one pending-rename file.
type PendingRename struct {
	// Written is when it was written; the engine drops it if it waits too long.
	Written time.Time `json:"written"`
	// Renames are in the order they were made.
	Renames []Rename `json:"renames"`
}

var renameSeq atomic.Int64

// nextRenameSeq returns a number larger than any this process gave out before
// and than the wall clock of an earlier moment, as a fixed-width string that sorts as a number.
func nextRenameSeq() string {
	now := time.Now().UnixNano()
	for {
		last := renameSeq.Load()
		next := now
		if next <= last {
			next = last + 1
		}
		if renameSeq.CompareAndSwap(last, next) {
			return fmt.Sprintf("%020d", next)
		}
	}
}

// isPendingRenameName reports whether a file name is a waiting (not claimed) pending-rename file.
func isPendingRenameName(n string) bool {
	return strings.HasPrefix(n, pendingRenamePrefix) && strings.HasSuffix(n, pendingRenameSuffix) && !strings.HasPrefix(n, claimedRenamePrefix)
}

// SavePendingRenames writes rs, in order, as a new pending-rename file
// (mode 0600), atomically. written is the time of writing; the zero value means now.
func SavePendingRenames(dir string, written time.Time, rs []Rename) error {
	_, err := SavePendingRenamesFile(dir, written, rs)
	return err
}

// SavePendingRenamesFile is SavePendingRenames that also returns the file it wrote ("" for nothing to write).
func SavePendingRenamesFile(dir string, written time.Time, rs []Rename) (string, error) {
	if len(rs) == 0 {
		return "", nil
	}
	if written.IsZero() {
		written = time.Now()
	}
	b, err := json.MarshalIndent(&PendingRename{Written: written, Renames: rs}, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, pendingRenamePrefix+nextRenameSeq()+pendingRenameSuffix)
	return path, paths.WriteFileAtomic(path, append(b, '\n'))
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
		return nil, fmt.Errorf("%s is damaged", filepath.Base(path))
	}
	if p.Written.IsZero() {
		if fi, err := os.Stat(path); err == nil {
			p.Written = fi.ModTime()
		}
	}
	return &p, nil
}

// seqOf returns the sequence number part of a pending or claimed file name.
func seqOf(name string) string {
	name = strings.TrimSuffix(name, pendingRenameSuffix)
	name = strings.TrimPrefix(name, claimedRenamePrefix)
	return strings.TrimPrefix(name, pendingRenamePrefix)
}

// ClaimPendingRenames takes the waiting pending-rename files for the caller by
// renaming each to a name of its own, so a file written meanwhile waits for the
// next claim instead of being lost or applied twice. It also returns files an
// interrupted claim left behind. The result is in seq order, which is the order
// the renames were made. The caller deletes the files when done, or gives them
// back with UnclaimRenames.
func ClaimPendingRenames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, de := range ents {
		n := de.Name()
		if de.IsDir() {
			continue
		}
		switch {
		case strings.HasPrefix(n, claimedRenamePrefix) && strings.HasSuffix(n, pendingRenameSuffix):
			out = append(out, filepath.Join(dir, n))
		case isPendingRenameName(n):
			aside := filepath.Join(dir, claimedRenamePrefix+seqOf(n)+pendingRenameSuffix)
			if err := os.Rename(filepath.Join(dir, n), aside); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return out, err
			}
			out = append(out, aside)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(seqOf(filepath.Base(a)), seqOf(filepath.Base(b))) })
	return out, nil
}

// UnclaimRenames gives claimed files back (the engine could not use them yet),
// under their original names, so they keep their place in the order.
func UnclaimRenames(files []string) error {
	var errs []error
	for _, f := range files {
		back := filepath.Join(filepath.Dir(f), pendingRenamePrefix+seqOf(filepath.Base(f))+pendingRenameSuffix)
		if err := os.Rename(f, back); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ReadClaimedRenames reads the claimed files in order and returns their
// renames and the oldest time of writing. A damaged file is skipped and its
// error returned.
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

// HasPendingRename reports whether a pending-rename file is waiting.
func HasPendingRename(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, de := range ents {
		if !de.IsDir() && isPendingRenameName(de.Name()) {
			return true
		}
	}
	return false
}

const renameIntentPrefix = "state.rename-intent."

// SaveRenameIntent writes a note, before config.toml is saved, that r is about
// to be made. An engine that sees an account vanish from the configuration
// sets its state aside only when a note (or a pending rename) names it as the
// old name of a rename; any other removal drops it. The caller deletes the note
// once the rename is handed over, or abandoned.
func SaveRenameIntent(dir string, r Rename) (string, error) {
	b, err := json.MarshalIndent(&PendingRename{Written: time.Now(), Renames: []Rename{r}}, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, renameIntentPrefix+nextRenameSeq()+pendingRenameSuffix)
	return path, paths.WriteFileAtomic(path, append(b, '\n'))
}

// RenameFroms returns the old names of the renames that are in progress or
// waiting: the intent notes, the pending files and the claimed ones.
func RenameFroms(dir string) map[string]bool {
	out := map[string]bool{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, de := range ents {
		n := de.Name()
		if de.IsDir() || !strings.HasSuffix(n, pendingRenameSuffix) {
			continue
		}
		if !strings.HasPrefix(n, pendingRenamePrefix) && !strings.HasPrefix(n, renameIntentPrefix) {
			continue
		}
		if p, err := ReadPendingRenameFile(filepath.Join(dir, n)); err == nil && p != nil {
			for _, r := range p.Renames {
				out[r.From] = true
			}
		}
	}
	return out
}
