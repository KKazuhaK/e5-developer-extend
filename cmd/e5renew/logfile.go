package main

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/KKazuhaK/e5-renew/internal/paths"
)

// maxLogFile is the size at which the run log is moved to <name>.1.
const maxLogFile = 5 << 20

// rotatingFile is an append-only log file that keeps one previous generation.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func openRotating(path string, max int64) (*rotatingFile, error) {
	r := &rotatingFile{path: path, max: max}
	if err := paths.EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, fi.Size()
	return nil
}

// Write implements io.Writer.
func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		_ = r.f.Close()
		_ = os.Remove(r.path + ".1")
		if err := os.Rename(r.path, r.path+".1"); err != nil {
			_ = os.Truncate(r.path, 0)
		}
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Close closes the file.
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// File returns the file currently written to (it changes when the log rotates).
func (r *rotatingFile) File() *os.File {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f
}

// openCrashLog opens <dir>/engine-crash.log next to the run log for crash
// dumps. It is appended to and capped at maxLogFile by starting over.
func openCrashLog(logFile string) (*os.File, error) {
	p := filepath.Join(filepath.Dir(logFile), "engine-crash.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > maxLogFile {
		_ = os.Remove(p)
	}
	return os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
