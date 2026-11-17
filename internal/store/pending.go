package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
)

// PendingImportFile is the name of the file an import leaves in the data
// directory when an engine is running. The engine keeps the state in memory and
// saves it over state.json, so the importer hands the imported state over
// instead of writing state.json itself; the engine adopts it on its next reload.
const PendingImportFile = "state.import.json"

// Pending import modes.
const (
	// PendingReplace replaces the engine's entries of the listed accounts.
	PendingReplace = "replace"
	// PendingMerge only adds entries for accounts the engine has none for.
	PendingMerge = "merge"
)

// adoptingPrefix starts the name a pending import gets while an engine
// applies it (see ClaimPendingImports).
const adoptingPrefix = "state.import.adopting-"

// PendingImport is the content of PendingImportFile.
type PendingImport struct {
	Mode string `json:"mode"`
	// Written is when the import wrote it; the engine drops entries that wait too long.
	Written  time.Time                `json:"written"`
	Accounts map[string]*AccountState `json:"accounts"`
}

// PendingImportPath returns where the pending import of the data directory dir is.
func PendingImportPath(dir string) string { return filepath.Join(dir, PendingImportFile) }

// SavePendingImport writes the pending import atomically (mode 0600). An
// import that is still waiting to be adopted is combined with p, so a second
// import before the engine looked does not lose the first.
func SavePendingImport(dir string, p *PendingImport) error {
	out := &PendingImport{Mode: p.Mode, Written: p.Written, Accounts: map[string]*AccountState{}}
	if out.Written.IsZero() {
		out.Written = time.Now()
	}
	for n, a := range p.Accounts {
		out.Accounts[n] = a
	}
	if old, err := ReadPendingImport(dir); err == nil && old != nil && p.Mode == PendingMerge {
		// A merge adds to what is waiting: keep its mode, and its entries win.
		out.Mode = old.Mode
		if !old.Written.IsZero() && old.Written.Before(out.Written) {
			out.Written = old.Written
		}
		for n, a := range old.Accounts {
			out.Accounts[n] = a
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(PendingImportPath(dir), append(b, '\n'))
}

// ReadPendingImport returns the pending import, nil when there is none. A
// file that cannot be read as one is reported as an error.
func ReadPendingImport(dir string) (*PendingImport, error) {
	return ReadPendingFile(PendingImportPath(dir))
}

// ReadPendingFile is ReadPendingImport for a file at path (a claimed one). A
// file without a time of writing gets the file's modification time.
func ReadPendingFile(path string) (*PendingImport, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p PendingImport
	if err := json.Unmarshal(b, &p); err != nil || (p.Mode != PendingReplace && p.Mode != PendingMerge) {
		return nil, fmt.Errorf("%s is damaged", PendingImportFile)
	}
	for n, a := range p.Accounts {
		if a == nil {
			delete(p.Accounts, n)
		}
	}
	if p.Written.IsZero() {
		if fi, err := os.Stat(path); err == nil {
			p.Written = fi.ModTime()
		}
	}
	return &p, nil
}

// ClaimPendingImports takes the pending import for the caller by renaming it
// to a name of its own, so an import that arrives while it is being applied
// writes a fresh PendingImportFile instead of being lost or applied twice. It
// returns the claimed files (also ones an earlier, interrupted claim left
// behind); the caller deletes each when it is done with it.
func ClaimPendingImports(dir string) ([]string, error) {
	var out []string
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	for _, de := range ents {
		if !de.IsDir() && strings.HasPrefix(de.Name(), adoptingPrefix) {
			out = append(out, filepath.Join(dir, de.Name()))
		}
	}
	aside := filepath.Join(dir, fmt.Sprintf("%s%d-%d", adoptingPrefix, os.Getpid(), time.Now().UnixNano()))
	switch err := os.Rename(PendingImportPath(dir), aside); {
	case err == nil:
		out = append(out, aside)
	case !errors.Is(err, fs.ErrNotExist):
		return out, err
	}
	return out, nil
}

// RemovePendingImport deletes the pending import file and any claimed one.
func RemovePendingImport(dir string) error {
	files := []string{PendingImportPath(dir)}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, de := range ents {
			if strings.HasPrefix(de.Name(), adoptingPrefix) {
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

// HasPendingImport reports whether a pending import file exists.
func HasPendingImport(dir string) bool {
	_, err := os.Stat(PendingImportPath(dir))
	return err == nil
}

// PendingImportBytes returns the pending import file as it is, nil when there is none.
func PendingImportBytes(dir string) []byte {
	b, err := os.ReadFile(PendingImportPath(dir))
	if err != nil {
		return nil
	}
	return b
}

// RenamePendingImport renames the account from to to in the import that waits
// to be adopted, if there is one. A rename that arrives while an engine is
// claiming the file does not reach it; the engine then drops that entry
// for an account that is not configured.
func RenamePendingImport(dir, from, to string) error {
	p, err := ReadPendingImport(dir)
	if err != nil || p == nil {
		return err
	}
	as, ok := p.Accounts[from]
	if !ok {
		return nil
	}
	delete(p.Accounts, from)
	p.Accounts[to] = as
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(PendingImportPath(dir), append(b, '\n'))
}
