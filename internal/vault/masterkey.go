package vault

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	// EnvMasterKey holds the base64 master key.
	EnvMasterKey = "E5RENEW_MASTER_KEY"
	// EnvMasterKeyFile names a file that holds the base64 master key; used
	// where systemd credentials are not available.
	EnvMasterKeyFile = "E5RENEW_MASTER_KEY_FILE"
	// CredentialName is the systemd credential file name.
	CredentialName = "e5renew-master-key"

	keyringService = "e5renew"
	keyringUser    = "master-key"
)

// Backend names accepted by ResolveMasterKey.
const (
	BackendAuto    = "auto"
	BackendKeyring = "keyring"
	BackendFile    = "file"
)

// Source says where a master key came from.
type Source string

const (
	SourceEnv        Source = "environment"
	SourceCredential Source = "systemd credential"
	SourceKeyFile    Source = "key file"
	SourceKeyring    Source = "system keyring"
)

// ErrNoMasterKey means no master key could be found or created.
var ErrNoMasterKey = errors.New("vault: no master key available")

// GenerateKey returns a new random master key.
func GenerateKey() ([]byte, error) {
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("vault: random key: %w", err)
	}
	return key, nil
}

// EncodeKey returns the base64 form used in the environment and credentials.
func EncodeKey(key []byte) string { return base64.StdEncoding.EncodeToString(key) }

// DecodeKey parses a base64 master key and checks its length.
func DecodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, errors.New("master key is not valid base64")
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("master key must decode to %d bytes, got %d", keySize, len(key))
	}
	return key, nil
}

const noKeyAdvice = "set " + EnvMasterKey + " (generate one with: e5renew vault init --print-key), " +
	"point " + EnvMasterKeyFile + " at a key file, or provide the systemd credential " + CredentialName + " via LoadCredential="

// KeyringTimeout bounds each system keyring call, so a locked Secret Service
// cannot hang the process.
var KeyringTimeout = 10 * time.Second

var (
	keyringGet = func(service, user string) (string, error) { return keyring.Get(service, user) }
	keyringSet = func(service, user, value string) error { return keyring.Set(service, user, value) }
)

var errKeyringTimeout = errors.New("the system keyring did not answer in time (is it locked?)")

func withTimeout[T any](f func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := f()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(KeyringTimeout):
		var zero T
		return zero, errKeyringTimeout
	}
}

func getFromKeyring() (string, error) {
	get := keyringGet // read here, not in the goroutine
	return withTimeout(func() (string, error) { return get(keyringService, keyringUser) })
}

func setInKeyring(value string) error {
	set := keyringSet
	_, err := withTimeout(func() (struct{}, error) {
		return struct{}{}, set(keyringService, keyringUser, value)
	})
	return err
}

func unavailable(what string, err error) error {
	return fmt.Errorf("%w: %s (%v); %s", ErrNoMasterKey, what, err, noKeyAdvice)
}

// checkNoOrphans refuses to create a key while encrypted data exists that a
// new key could never read.
func checkNoOrphans(vaultDir string) error {
	if vaultDir == "" {
		return nil
	}
	has, err := hasData(vaultDir)
	if err != nil {
		return fmt.Errorf("%w: cannot check existing vault data (%v); refusing to create a new key", ErrNoMasterKey, err)
	}
	if has {
		return fmt.Errorf("%w: the vault has data but no master key was found; restore the old key via %s or the system keyring, "+
			"or run `e5renew vault reset` to wipe the vault", ErrNoMasterKey, EnvMasterKey)
	}
	return nil
}

// ResolveMasterKey finds the master key for backend. With "auto" it creates
// and stores a new key in the system keyring when none exists anywhere and the
// vault directory vaultDir holds no data yet. An empty vaultDir skips that check.
func ResolveMasterKey(backend, vaultDir string) ([]byte, Source, error) {
	switch backend {
	case BackendAuto, "":
	case BackendKeyring, BackendFile:
	default:
		return nil, "", fmt.Errorf("vault: unknown backend %q", backend)
	}

	if backend != BackendKeyring {
		if key, src, err := fromEnvOrCredential(); key != nil || err != nil {
			return key, src, err
		}
		if backend == BackendFile {
			return nil, "", fmt.Errorf("%w: backend \"file\" needs a key: %s", ErrNoMasterKey, noKeyAdvice)
		}
	}

	s, err := getFromKeyring()
	switch {
	case err == nil:
		key, derr := DecodeKey(s)
		if derr != nil {
			return nil, "", fmt.Errorf("vault: master key in the system keyring is invalid: %w", derr)
		}
		return key, SourceKeyring, nil
	case !errors.Is(err, keyring.ErrNotFound):
		return nil, "", unavailable("the system keyring is unavailable", err)
	}

	if backend == BackendKeyring {
		return nil, "", fmt.Errorf("%w: no key in the system keyring; run: e5renew vault init", ErrNoMasterKey)
	}
	if err := checkNoOrphans(vaultDir); err != nil {
		return nil, "", err
	}
	if err := createKeyringKey(); err != nil {
		return nil, "", err
	}
	// Re-read: another process may have won the race to store its key.
	s, err = getFromKeyring()
	if err != nil {
		return nil, "", unavailable("cannot read back the new key from the system keyring", err)
	}
	key, err := DecodeKey(s)
	if err != nil {
		return nil, "", fmt.Errorf("vault: master key in the system keyring is invalid: %w", err)
	}
	return key, SourceKeyring, nil
}

// createKeyringKey stores a fresh key unless one appeared in the meantime.
func createKeyringKey() error {
	key, err := GenerateKey()
	if err != nil {
		return err
	}
	if _, err := getFromKeyring(); err == nil {
		return nil
	}
	if err := setInKeyring(EncodeKey(key)); err != nil {
		return unavailable("cannot store a new key in the system keyring", err)
	}
	return nil
}

func fromEnvOrCredential() ([]byte, Source, error) {
	if v := os.Getenv(EnvMasterKey); v != "" {
		key, err := DecodeKey(v)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", EnvMasterKey, err)
		}
		return key, SourceEnv, nil
	}
	if p := os.Getenv(EnvMasterKeyFile); p != "" {
		if fi, err := os.Stat(p); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
			slog.Warn("the master key file is readable by other users; fix it with: chmod 600 "+p, "mode", fi.Mode().Perm().String())
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, "", fmt.Errorf("%s: read the key file: %w", EnvMasterKeyFile, err)
		}
		key, err := DecodeKey(string(data))
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", EnvMasterKeyFile, err)
		}
		return key, SourceKeyFile, nil
	}
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		p := filepath.Join(dir, CredentialName)
		data, err := os.ReadFile(p)
		switch {
		case err == nil:
			key, derr := DecodeKey(string(data))
			if derr != nil {
				return nil, "", fmt.Errorf("%s: %w", p, derr)
			}
			return key, SourceCredential, nil
		case !errors.Is(err, os.ErrNotExist):
			return nil, "", fmt.Errorf("vault: read %s: %w", p, err)
		}
	}
	return nil, "", nil
}

// InitKeyring generates a master key and stores it in the system keyring.
// It refuses to replace an existing key, and to create one while the vault in
// vaultDir already holds data.
func InitKeyring(vaultDir string) error {
	if _, err := getFromKeyring(); err == nil {
		return ErrKeyExists
	} else if !errors.Is(err, keyring.ErrNotFound) {
		return unavailable("the system keyring is unavailable", err)
	}
	if err := checkNoOrphans(vaultDir); err != nil {
		return err
	}
	return createKeyringKey()
}

// ErrKeyExists means the system keyring already holds a master key.
var ErrKeyExists = errors.New("vault: a master key already exists in the system keyring")

// LookupMasterKey finds an existing master key for backend like
// ResolveMasterKey does, but never creates one. It returns ErrNoMasterKey
// (wrapped) when none exists.
func LookupMasterKey(backend string) ([]byte, Source, error) {
	switch backend {
	case BackendAuto, "", BackendKeyring, BackendFile:
	default:
		return nil, "", fmt.Errorf("vault: unknown backend %q", backend)
	}
	if backend != BackendKeyring {
		if key, src, err := fromEnvOrCredential(); key != nil || err != nil {
			return key, src, err
		}
		if backend == BackendFile {
			return nil, "", fmt.Errorf("%w: backend \"file\" needs a key: %s", ErrNoMasterKey, noKeyAdvice)
		}
	}
	s, err := getFromKeyring()
	switch {
	case err == nil:
		key, derr := DecodeKey(s)
		if derr != nil {
			return nil, "", fmt.Errorf("vault: master key in the system keyring is invalid: %w", derr)
		}
		return key, SourceKeyring, nil
	case errors.Is(err, keyring.ErrNotFound):
		return nil, "", fmt.Errorf("%w: none found in the environment, credentials or system keyring", ErrNoMasterKey)
	}
	return nil, "", unavailable("the system keyring is unavailable", err)
}
