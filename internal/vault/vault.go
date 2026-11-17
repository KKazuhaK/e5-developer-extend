// Package vault stores secrets in AES-256-GCM encrypted files under the data
// directory. A single 32-byte master key (environment, systemd credential or
// system keyring) protects all of them.
package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/KKazuhaK/e5-renew/internal/paths"
)

const (
	formatVersion byte = 1
	keySize            = 32
)

var (
	// ErrNotFound means the key has no stored value.
	ErrNotFound = errors.New("vault: not found")
	// ErrWrongKey means decryption failed: the master key is not the one the
	// data was written with, or the file was altered or moved.
	ErrWrongKey = errors.New("vault: cannot decrypt (the master key may have changed because of a different machine or a cleared keyring, or the file was altered)")
	// ErrCorrupt means a vault file is not in the expected format.
	ErrCorrupt = errors.New("vault: corrupt file")
	// ErrInvalidKeyName means a vault key name is not allowed.
	ErrInvalidKeyName = errors.New("vault: invalid key name")
)

var keyNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$`)

// SecretKey returns the key name of an account's client secret or password.
func SecretKey(account string) string { return "secret/" + account }

// TokenCacheKey returns the key name of an account's MSAL token cache.
func TokenCacheKey(account string) string { return "tokencache/" + account }

// Vault is an encrypted key-value store. It is safe for concurrent use.
type Vault struct {
	dir    string
	keyFn  func() ([]byte, error)
	mu     sync.Mutex
	key    []byte
	loaded bool
}

// New returns a vault in dir whose master key is obtained lazily from keyFn
// on the first operation that needs it.
func New(dir string, keyFn func() ([]byte, error)) *Vault {
	return &Vault{dir: dir, keyFn: keyFn}
}

// Open returns a vault in the default vault directory, using the master key
// resolved for the given backend (auto, keyring or file).
func Open(backend string) (*Vault, error) {
	dir, err := paths.VaultDir()
	if err != nil {
		return nil, err
	}
	return New(dir, func() ([]byte, error) {
		key, _, err := ResolveMasterKey(backend, dir)
		return key, err
	}), nil
}

func (v *Vault) masterKey() ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.loaded {
		return v.key, nil
	}
	key, err := v.keyFn()
	if err != nil {
		return nil, err
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("vault: master key must be %d bytes, got %d", keySize, len(key))
	}
	v.key, v.loaded = key, true
	return key, nil
}

func (v *Vault) path(name string) (string, error) {
	if !keyNameRe.MatchString(name) {
		return "", fmt.Errorf("%w: %q", ErrInvalidKeyName, name)
	}
	return filepath.Join(v.dir, filepath.FromSlash(name)+".bin"), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Has reports whether a value is stored for name. It does not need the master key.
func (v *Vault) Has(name string) (bool, error) {
	p, err := v.path(name)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// List returns the key names of every stored value, sorted. It only looks at
// the file names, so it does not need the master key. Lock files, temporary
// files and anything else that is not a vault entry are ignored.
func (v *Vault) List() ([]string, error) {
	var keys []string
	err := filepath.WalkDir(v.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == v.dir {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() || filepath.Ext(p) != ".bin" {
			return nil
		}
		rel, err := filepath.Rel(v.dir, p)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.ToSlash(rel), ".bin")
		if keyNameRe.MatchString(name) {
			keys = append(keys, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("vault: list %s: %w", v.dir, err)
	}
	sort.Strings(keys)
	return keys, nil
}

// Get returns the decrypted value stored under name.
func (v *Vault) Get(name string) ([]byte, error) {
	p, err := v.path(name)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("vault: read %s: %w", name, err)
	}
	key, err := v.masterKey()
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(raw) < 1+gcm.NonceSize()+gcm.Overhead() || raw[0] != formatVersion {
		return nil, fmt.Errorf("%w: %s", ErrCorrupt, name)
	}
	nonce, ct := raw[1:1+gcm.NonceSize()], raw[1+gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, []byte(name))
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", ErrWrongKey, name)
	}
	return plain, nil
}

// Snapshot returns the stored, still encrypted, bytes of name and whether the
// entry exists. It does not need the master key, so it works for entries that
// cannot be decrypted too. Pass the result to Restore to undo a change.
func (v *Vault) Snapshot(name string) (raw []byte, exists bool, err error) {
	p, err := v.path(name)
	if err != nil {
		return nil, false, err
	}
	raw, err = os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("vault: read %s: %w", name, err)
	}
	return raw, true, nil
}

// Restore puts back what Snapshot returned: the bytes as they were, or no
// entry when there was none.
func (v *Vault) Restore(name string, raw []byte, existed bool) error {
	if !existed {
		if err := v.Delete(name); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		return nil
	}
	p, err := v.path(name)
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(p, raw)
}

// Put encrypts and stores value under name, replacing any previous value.
func (v *Vault) Put(name string, value []byte) error {
	p, err := v.path(name)
	if err != nil {
		return err
	}
	key, err := v.masterKey()
	if err != nil {
		return err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("vault: random nonce: %w", err)
	}
	out := make([]byte, 0, 1+len(nonce)+len(value)+gcm.Overhead())
	out = append(out, formatVersion)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, value, []byte(name))
	return paths.WriteFileAtomic(p, out)
}

// Delete removes the value stored under name.
func (v *Vault) Delete(name string) error {
	p, err := v.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return fmt.Errorf("vault: delete %s: %w", name, err)
	}
	// The lock file is left in place on purpose: removing it while another
	// process waits on it would let two processes hold "the same" lock.
	return nil
}

func (v *Vault) lockPath(name string) string {
	return filepath.Join(v.dir, filepath.FromSlash(name)+".lock")
}

// Lock takes a cross-process advisory lock for name and returns the release function.
func (v *Vault) Lock(ctx context.Context, name string) (func(), error) {
	if _, err := v.path(name); err != nil {
		return nil, err
	}
	return paths.Lock(ctx, v.lockPath(name))
}

// hasData reports whether any encrypted vault file exists under dir. A missing
// dir means no data; any other walk error is returned so callers never assume
// an empty vault they could not actually read.
func hasData(dir string) (bool, error) {
	found := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == dir {
				return fs.SkipAll
			}
			return err
		}
		if !d.IsDir() && filepath.Ext(p) == ".bin" {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("vault: inspect %s: %w", dir, err)
	}
	return found, nil
}

// Reset deletes everything in the vault directory dir. A new master key can
// be generated afterwards.
func Reset(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("vault: reset: %w", err)
	}
	return nil
}

// IsNotFound is a convenience for errors.Is(err, ErrNotFound).
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
