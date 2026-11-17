package services

import (
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
)

// LogService reads the call log.
type LogService struct{ b *Backend }

// NewLogService returns the service.
func NewLogService(b *Backend) *LogService { return &LogService{b} }

// Read returns log entries, newest first. account "" means all accounts,
// sinceHours 0 means no time limit and limit 0 means no limit.
func (s *LogService) Read(account string, sinceHours int, limit int) ([]LogEntry, error) {
	list, err := s.b.Ops.ReadLogs(ops.LogFilter{
		Account: account, Since: time.Duration(sinceHours) * time.Hour, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]LogEntry, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		en := list[i]
		le := LogEntry{
			TS: en.TS.Format(time.RFC3339), Account: en.Account, API: en.API, Status: en.Status,
			Class: en.Class, LatencyMs: en.LatencyMS, Event: en.Event, Detail: en.Message,
		}
		out = append(out, le)
	}
	return out, nil
}

// APIStats returns the per-API call counters of one account, or summed over
// all accounts when account is empty. Only APIs that were called are listed.
func (s *LogService) APIStats(account string) ([]APIStat, error) { return s.b.Ops.APIStats(account) }
