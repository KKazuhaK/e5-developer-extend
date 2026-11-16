package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/KKazuhaK/e5-renew/internal/paths"
)

// ErrRunning means another engine already holds the single-instance lock.
var ErrRunning = errors.New("an engine is already running")

func lockPath(dataDir string) string { return filepath.Join(dataDir, "engine.lock") }

// AcquireLock takes the single-instance lock without waiting. The returned
// function releases it.
func AcquireLock(dataDir string) (func(), error) {
	if err := paths.EnsureDir(dataDir); err != nil {
		return nil, err
	}
	fl := flock.New(lockPath(dataDir))
	ok, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", lockPath(dataDir), err)
	}
	if !ok {
		return nil, ErrRunning
	}
	return func() { _ = fl.Unlock() }, nil
}

// Running reports whether an engine currently holds the lock.
func Running(dataDir string) bool {
	if _, err := os.Stat(lockPath(dataDir)); err != nil {
		return false
	}
	release, err := AcquireLock(dataDir)
	if err != nil {
		return errors.Is(err, ErrRunning)
	}
	release()
	return false
}

func heartbeatPath(dataDir string) string { return filepath.Join(dataDir, "heartbeat") }

func writeHeartbeat(dataDir string, t time.Time) error {
	if err := paths.EnsureDir(dataDir); err != nil {
		return err
	}
	tmp := heartbeatPath(dataDir) + ".tmp-hb"
	if err := os.WriteFile(tmp, []byte(t.Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, heartbeatPath(dataDir))
}

// ReadHeartbeat returns the time of the engine's last tick.
func ReadHeartbeat(dataDir string) (time.Time, error) {
	b, err := os.ReadFile(heartbeatPath(dataDir))
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
}

func triggerDir(dataDir string) string { return filepath.Join(dataDir, "trigger") }

func writeTrigger(dataDir, name string) error {
	dir := triggerDir(dataDir)
	if err := paths.EnsureDir(dir); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), nil, 0o600)
}

// TriggerRun asks a running engine to start a round for account.
func TriggerRun(dataDir, account string) error { return writeTrigger(dataDir, account) }

// TriggerResume asks the engine to clear a pause or attention state.
func TriggerResume(dataDir, account string) error { return writeTrigger(dataDir, account+".resume") }

// TriggerReload asks the engine to reload the configuration now.
func TriggerReload(dataDir string) error { return writeTrigger(dataDir, ".reload") }

// WriteHeartbeat records t as the engine's last tick; exported for tests of
// tools that read the heartbeat.
func WriteHeartbeat(dataDir string, t time.Time) error { return writeHeartbeat(dataDir, t) }
