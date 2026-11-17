package notify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
)

const statusFile = "notify-status.json"

// ChannelStatus is how the deliveries of one channel have gone. It never
// holds an address or a token: Error is the redacted delivery error.
type ChannelStatus struct {
	At    time.Time `json:"at"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	// Failures counts the deliveries that failed in a row; LastOK is the last success.
	Failures int       `json:"failures,omitempty"`
	LastOK   time.Time `json:"lastOk,omitzero"`
}

var statusMu sync.Mutex

// withStatusLock runs f with the status file locked against other processes
// (the engine, the app and the CLI all record results); if the lock cannot
// be had in time, f is skipped: a lost status line is better than a stall.
func withStatusLock(dir string, f func(path string)) {
	if dir == "" {
		return
	}
	statusMu.Lock()
	defer statusMu.Unlock()
	path := filepath.Join(dir, statusFile)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	unlock, err := paths.Lock(ctx, path+".lock")
	if err != nil {
		return
	}
	defer unlock()
	f(path)
}

func readStatusFile(path string) map[string]ChannelStatus {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	m := map[string]ChannelStatus{}
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func writeStatusFile(path string, m map[string]ChannelStatus) {
	if b, err := json.Marshal(m); err == nil {
		_ = paths.WriteFileAtomic(path, b)
	}
}

// ReadStatus returns the recorded status of every channel, or nil.
func ReadStatus(dir string) map[string]ChannelStatus {
	if dir == "" {
		return nil
	}
	return readStatusFile(filepath.Join(dir, statusFile))
}

// RecordStatus notes how a delivery through channel id went.
func RecordStatus(dir, id string, err error) {
	withStatusLock(dir, func(path string) {
		m := readStatusFile(path)
		if m == nil {
			m = map[string]ChannelStatus{}
		}
		prev := m[id]
		now := time.Now()
		r := ChannelStatus{At: now, OK: err == nil, LastOK: prev.LastOK}
		if err == nil {
			r.LastOK = now
		} else {
			r.Failures = prev.Failures + 1
			e := err.Error()
			if i := strings.IndexByte(e, '\n'); i >= 0 {
				e = e[:i]
			}
			if len(e) > 300 {
				e = e[:300]
			}
			r.Error = e
		}
		m[id] = r
		writeStatusFile(path, m)
	})
}

// ForgetStatus drops the status of a removed channel.
func ForgetStatus(dir, id string) {
	withStatusLock(dir, func(path string) {
		m := readStatusFile(path)
		if _, ok := m[id]; !ok {
			return
		}
		delete(m, id)
		writeStatusFile(path, m)
	})
}
