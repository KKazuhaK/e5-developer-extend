package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// SyncWrites makes WriteFileAtomic flush to disk before renaming. Only tests
// that write thousands of files switch it off.
var SyncWrites = true

// WriteFileAtomic writes data to path through a temporary file in the same
// directory, syncs it and renames it into place. The result has mode 0600.
func WriteFileAtomic(path string, data []byte) error {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// WriteFileAtomicInPlace is WriteFileAtomic for a file the user chose the
// location of: the directory must already exist and is left as it is, where
// WriteFileAtomic would create it and tighten it to 0700.
func WriteFileAtomicInPlace(path string, data []byte) error {
	return writeFileAtomic(path, data)
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if SyncWrites {
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			cleanup()
			return fmt.Errorf("sync %s: %w", tmpName, err)
		}
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := renameRetry(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename to %s: %w", path, err)
	}
	if SyncWrites {
		syncDir(dir)
	}
	return nil
}

// syncDir flushes the directory entry; best effort, not supported on Windows.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// renameRetry renames, retrying briefly on Windows where the target may be
// held open for a moment by a reader or a virus scanner.
func renameRetry(from, to string) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = os.Rename(from, to); err == nil || runtime.GOOS != "windows" {
			return err
		}
		time.Sleep(time.Duration(10<<i) * time.Millisecond)
	}
	return err
}

// RemoveStaleTemp deletes leftover temporary files of interrupted atomic
// writes (named *.tmp-*) in dir that are older than olderThan.
func RemoveStaleTemp(dir string, olderThan time.Duration) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range ents {
		n := de.Name()
		// Only names produced by this program (<file>.tmp-<suffix>); never touch
		// other files the user may keep in the config directory.
		if de.IsDir() || !strings.Contains(n, ".tmp-") {
			continue
		}
		if fi, err := de.Info(); err == nil && time.Since(fi.ModTime()) > olderThan {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}
