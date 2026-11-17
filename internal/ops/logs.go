package ops

import (
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
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

// AccountLogFilter selects the log lines of the account called name: those
// written under its key, so the lines from before a rename are included. A name
// no configured account has selects the lines written under that name. An empty
// name selects everything.
func (o *Ops) AccountLogFilter(name string) store.Filter {
	if name == "" {
		return store.Filter{}
	}
	if cfg, _, err := o.LoadConfig(); err == nil {
		if a, ok := cfg.Find(name); ok {
			return store.Filter{Account: a.Key()}
		}
	}
	return store.Filter{Name: name}
}

// ReadLogs returns the call log, oldest first.
func (o *Ops) ReadLogs(f LogFilter) ([]store.Entry, error) {
	st, err := o.Store()
	if err != nil {
		return nil, err
	}
	sf := o.AccountLogFilter(f.Account)
	sf.Limit = f.Limit
	if f.Since > 0 {
		sf.Since = time.Now().Add(-f.Since)
	}
	return st.Read(sf)
}
