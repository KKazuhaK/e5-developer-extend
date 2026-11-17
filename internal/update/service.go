package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
)

// CacheFile is the name of the update check's memory in the data directory. It
// is a file of its own, not part of state.json: the command line and the
// window check too, and state.json belongs to the engine alone, which would
// overwrite what anyone else wrote.
const CacheFile = "update.json"

// LoadCache reads update.json; a missing or damaged file gives an empty cache.
func LoadCache(dataDir string) Cache {
	b, err := os.ReadFile(filepath.Join(dataDir, CacheFile))
	if err != nil {
		return Cache{}
	}
	var c Cache
	if json.Unmarshal(b, &c) != nil || c.Version > CacheVersion {
		return Cache{}
	}
	return c
}

// SaveCache writes update.json atomically.
func SaveCache(dataDir string, c Cache) error {
	b, err := MarshalCache(c)
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(filepath.Join(dataDir, CacheFile), b)
}

// Info is what is known about the newest release.
type Info struct {
	// Current is the running version, Channel the channel in force.
	Current string `json:"current"`
	Channel string `json:"channel"`
	// Dev is true for a development build, which never offers an upgrade.
	Dev bool `json:"dev"`
	// Latest is the newest release the channel offers, nil when none is known.
	Latest *Latest `json:"latest"`
	// Available is true when Latest is newer than Current.
	Available bool `json:"available"`
	// CheckedAt is when GitHub last answered, zero when never.
	CheckedAt time.Time `json:"checkedAt"`
}

// Service is the update check of one program: it ties the checker to update.json.
type Service struct {
	DataDir string
	// Current is the running version (internal/version.Version).
	Current string
	// Channel is ChannelStable or ChannelPrerelease; stable when empty.
	Channel string
	Checker *Checker
	// Now is time.Now when nil.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) channel() string {
	if s.Channel == ChannelPrerelease {
		return ChannelPrerelease
	}
	return ChannelStable
}

func (s *Service) info(c Cache) Info {
	in := Info{Current: s.Current, Channel: s.channel(), Dev: IsDevBuild(s.Current), CheckedAt: c.CheckedAt}
	if c.Channel == s.channel() && c.Latest != nil {
		l := *c.Latest
		in.Latest = &l
		in.Available = c.Available(s.Current)
	}
	return in
}

// Cached is what the last check found; it makes no request.
func (s *Service) Cached() Info { return s.info(LoadCache(s.DataDir)) }

// Due reports whether a check on its own is due now. A development build never is.
func (s *Service) Due() bool {
	return !IsDevBuild(s.Current) && LoadCache(s.DataDir).Due(s.now(), s.channel())
}

// Next is when the next check on its own is due.
func (s *Service) Next() time.Time { return LoadCache(s.DataDir).Next(s.channel()) }

// Check asks GitHub when a check is due, or when force is set, and otherwise
// answers from the cache. A development build is only checked when forced.
// On failure the info of the cache comes back with the error.
func (s *Service) Check(ctx context.Context, force bool) (Info, error) {
	c := LoadCache(s.DataDir)
	if !force && !s.Due() {
		return s.info(c), nil
	}
	if s.Checker == nil {
		return s.info(c), errors.New("no update checker")
	}
	next, _, err := s.Checker.Check(ctx, s.channel(), c, s.now())
	if serr := SaveCache(s.DataDir, next); serr != nil && err == nil {
		err = serr
	}
	return s.info(next), err
}

// Release fetches the release to install, fresh (no cache), and requires it to
// be newer than the running version.
func (s *Service) Release(ctx context.Context) (Release, error) {
	if s.Checker == nil {
		return Release{}, errors.New("no update checker")
	}
	rel, err := s.Checker.Fetch(ctx, s.channel())
	if err != nil {
		return Release{}, err
	}
	if rel == nil {
		return Release{}, errors.New("no release has been published")
	}
	return *rel, nil
}
