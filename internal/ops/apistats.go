package ops

import (
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// APIStatRow is the call statistics of one API: of one account, or summed over
// all accounts of the configuration when no account is named.
type APIStatRow struct {
	ID         string  `json:"id"`
	Category   string  `json:"category"`
	Name       string  `json:"name"`
	OK         int     `json:"ok"`
	Failed     int     `json:"failed"`
	Total      int     `json:"total"`
	LastClass  string  `json:"lastClass"`
	LastStatus int     `json:"lastStatus"`
	LastAt     *string `json:"lastAt"`
}

// APIStats reads the per-API counters the engine keeps in state.json. Only
// APIs that were called at least once are listed, in catalog order. It never
// touches the network.
func (o *Ops) APIStats(account string) ([]APIStatRow, error) {
	apis, err := catalog.Load()
	if err != nil {
		return nil, err
	}
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return nil, err
	}
	if account != "" {
		if _, err := o.FindAccount(cfg, account); err != nil {
			return nil, err
		}
	}
	// Only accounts that are in the configuration count: the state may still
	// hold one that was removed.
	inConfig := map[string]bool{}
	for _, a := range cfg.Accounts {
		inConfig[a.Name] = true
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return nil, err
	}
	state, err := store.New(dataDir).LoadState()
	if err != nil {
		return nil, err
	}
	sum := map[string]*store.APIStat{}
	for name, as := range state.Accounts {
		if !inConfig[name] || (account != "" && name != account) {
			continue
		}
		for id, st := range as.APIStats {
			if st == nil {
				continue
			}
			s := sum[id]
			if s == nil {
				s = &store.APIStat{}
				sum[id] = s
			}
			s.OK += st.OK
			s.Failed += st.Failed
			if st.LastAt.After(s.LastAt) {
				s.LastAt, s.LastClass, s.LastStatus = st.LastAt, st.LastClass, st.LastStatus
			}
		}
	}
	rows := []APIStatRow{}
	for _, a := range apis {
		s := sum[a.ID]
		if s == nil || s.Total() == 0 {
			continue
		}
		rows = append(rows, APIStatRow{
			ID: a.ID, Category: a.Category, Name: a.Name, OK: s.OK, Failed: s.Failed, Total: s.Total(),
			LastClass: s.LastClass, LastStatus: s.LastStatus, LastAt: rfc3339(s.LastAt),
		})
	}
	return rows, nil
}
