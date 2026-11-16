package ops

import (
	"time"

	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/store"
)

// Store returns the store under the data directory.
func (o *Ops) Store() (*store.Store, error) {
	dataDir, err := paths.DataDir()
	if err != nil {
		return nil, err
	}
	return store.New(dataDir), nil
}

// LogFilter selects log entries. Zero fields match everything.
type LogFilter struct {
	Account string
	Since   time.Duration // entries newer than now minus this; 0 for all
	Limit   int           // newest entries only; 0 for all
}

// ReadLogs returns the call log, oldest first.
func (o *Ops) ReadLogs(f LogFilter) ([]store.Entry, error) {
	st, err := o.Store()
	if err != nil {
		return nil, err
	}
	sf := store.Filter{Account: f.Account, Limit: f.Limit}
	if f.Since > 0 {
		sf.Since = time.Now().Add(-f.Since)
	}
	return st.Read(sf)
}
