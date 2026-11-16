package paths

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// LockTimeout bounds how long Lock waits for another process.
var LockTimeout = 30 * time.Second

// Lock takes an exclusive advisory lock on the file at path (created if
// needed) and returns the function that releases it.
func Lock(ctx context.Context, path string) (unlock func(), err error) {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, LockTimeout)
	defer cancel()
	fl := flock.New(path)
	ok, err := fl.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if !ok {
		return nil, fmt.Errorf("lock %s: not acquired", path)
	}
	return func() { _ = fl.Unlock() }, nil
}

// ConfigLockFile returns the lock file guarding config changes.
func ConfigLockFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".config.lock"), nil
}
