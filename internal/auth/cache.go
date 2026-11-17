package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"

	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// cacheLocks serializes cache reads and writes per vault key within the process.
var cacheLocks sync.Map // string -> *sync.Mutex

func lockFor(key string) *sync.Mutex {
	m, _ := cacheLocks.LoadOrStore(key, new(sync.Mutex))
	return m.(*sync.Mutex)
}

type recoverKey struct{}

// withRecovery marks ctx so an unreadable token cache is discarded instead of
// failing the call; used by Login, which can rebuild the cache.
func withRecovery(ctx context.Context) context.Context {
	return context.WithValue(ctx, recoverKey{}, true)
}

// Locker is implemented by stores that offer cross-process locks.
type Locker interface {
	Lock(ctx context.Context, name string) (unlock func(), err error)
}

// vaultCache persists the MSAL token cache in the vault.
//
// Writes are optimistic. Replace remembers what it read (a digest, or that
// nothing was stored); Export writes only when the stored value is still that.
// Another writer in between, such as a backup import or another process,
// wins, and this cache simply reloads the new value on its next Replace.
type vaultCache struct {
	store Store
	key   string

	mu   sync.Mutex // guards seen
	seen *stored
}

// stored is what the cache last saw in the vault.
type stored struct {
	absent bool
	digest [sha256.Size]byte
}

func storedOf(data []byte) *stored { return &stored{digest: sha256.Sum256(data)} }

func (c *vaultCache) remember(s *stored) {
	c.mu.Lock()
	c.seen = s
	c.mu.Unlock()
}

func newVaultCache(store Store, account string) *vaultCache {
	return &vaultCache{store: store, key: vault.TokenCacheKey(account)}
}

var _ cache.ExportReplace = (*vaultCache)(nil)

// lock takes the in-process mutex and, when the store supports it, the
// cross-process file lock.
func (c *vaultCache) lock(ctx context.Context) (func(), error) {
	mu := lockFor(c.key)
	mu.Lock()
	unlock := mu.Unlock
	if l, ok := c.store.(Locker); ok {
		release, err := l.Lock(ctx, c.key)
		if err != nil {
			mu.Unlock()
			return nil, err
		}
		unlock = func() { release(); mu.Unlock() }
	}
	return unlock, nil
}

var emptyCache = []byte("{}")

// Replace loads the stored cache into msal. An absent cache resets msal's
// in-memory copy so a removed cache cannot come back.
func (c *vaultCache) Replace(ctx context.Context, u cache.Unmarshaler, _ cache.ReplaceHints) error {
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	c.remember(nil) // until this read succeeds, there is nothing to compare a write with
	data, err := c.store.Get(c.key)
	switch {
	case err == nil:
		c.remember(storedOf(data))
		return u.Unmarshal(data)
	case errors.Is(err, vault.ErrNotFound):
		c.remember(&stored{absent: true})
		return u.Unmarshal(emptyCache)
	case errors.Is(err, vault.ErrWrongKey), errors.Is(err, vault.ErrCorrupt):
		if ctx.Value(recoverKey{}) == nil {
			return fmt.Errorf("%w: the stored token cache cannot be read: %w", ErrLoginRequired, err)
		}
		slog.Warn("discarding unreadable token cache", "key", c.key, "reason", err)
		if derr := c.store.Delete(c.key); derr != nil && !errors.Is(derr, vault.ErrNotFound) {
			return derr
		}
		c.remember(&stored{absent: true})
		return u.Unmarshal(emptyCache)
	default:
		return err
	}
}

// Export writes the msal cache back to the vault.
func (c *vaultCache) Export(ctx context.Context, m cache.Marshaler, _ cache.ExportHints) error {
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	data, err := m.Marshal()
	if err != nil {
		return err
	}
	c.mu.Lock()
	seen := c.seen
	c.mu.Unlock()
	if seen != nil {
		changed, err := c.changedSince(seen)
		if err != nil {
			return err
		}
		if changed {
			slog.Debug("token cache was changed by someone else; not overwriting it", "key", c.key)
			return nil
		}
	}
	if err := c.store.Put(c.key, data); err != nil {
		return err
	}
	c.remember(storedOf(data))
	return nil
}

// changedSince reports whether the stored value differs from seen. A value
// that cannot be read counts as changed: it is not what was loaded.
func (c *vaultCache) changedSince(seen *stored) (bool, error) {
	now, err := c.store.Get(c.key)
	switch {
	case err == nil:
		return seen.absent || storedOf(now).digest != seen.digest, nil
	case errors.Is(err, vault.ErrNotFound):
		return !seen.absent, nil
	case errors.Is(err, vault.ErrWrongKey), errors.Is(err, vault.ErrCorrupt):
		return true, nil
	default:
		return false, err
	}
}

func (c *vaultCache) clear() error {
	unlock, err := c.lock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	if err := c.store.Delete(c.key); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return err
	}
	c.remember(&stored{absent: true})
	return nil
}
