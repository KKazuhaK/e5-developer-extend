package vault

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func testVault(t *testing.T) (*Vault, string) {
	t.Helper()
	dir := t.TempDir()
	key, _ := GenerateKey()
	return New(dir, func() ([]byte, error) { return key, nil }), dir
}

func TestRoundTrip(t *testing.T) {
	v, dir := testVault(t)
	if _, err := v.Get("secret/main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: got %v", err)
	}
	if ok, _ := v.Has("secret/main"); ok {
		t.Fatal("Has before Put")
	}
	if err := v.Put("secret/main", []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	got, err := v.Get("secret/main")
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if ok, _ := v.Has("secret/main"); !ok {
		t.Fatal("Has after Put")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "secret", "main.bin"))
	if bytes.Contains(raw, []byte("hunter2")) || raw[0] != 1 {
		t.Fatal("file is not encrypted or has a wrong version byte")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, "secret", "main.bin"))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
	}
	// Overwrite uses a fresh nonce.
	if err := v.Put("secret/main", []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(filepath.Join(dir, "secret", "main.bin"))
	if bytes.Equal(raw, raw2) {
		t.Error("nonce reused")
	}
	if err := v.Delete("secret/main"); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete("secret/main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestWrongKey(t *testing.T) {
	v, dir := testVault(t)
	if err := v.Put("secret/a", []byte("x")); err != nil {
		t.Fatal(err)
	}
	other, _ := GenerateKey()
	v2 := New(dir, func() ([]byte, error) { return other, nil })
	if _, err := v2.Get("secret/a"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("got %v, want ErrWrongKey", err)
	}
}

func TestAADPreventsSwap(t *testing.T) {
	v, dir := testVault(t)
	_ = v.Put("secret/a", []byte("alpha"))
	_ = v.Put("secret/b", []byte("beta"))
	a := filepath.Join(dir, "secret", "a.bin")
	b := filepath.Join(dir, "secret", "b.bin")
	data, _ := os.ReadFile(a)
	if err := os.WriteFile(b, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get("secret/b"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("swapped file: got %v, want ErrWrongKey", err)
	}
}

func TestCorruptAndInvalidNames(t *testing.T) {
	v, dir := testVault(t)
	_ = os.MkdirAll(filepath.Join(dir, "secret"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "secret", "x.bin"), []byte{9, 1, 2}, 0o600)
	if _, err := v.Get("secret/x"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v, want ErrCorrupt", err)
	}
	for _, name := range []string{"", "../x", "a//b", "a/../b", "/abs", "a b", "a.bin"} {
		if err := v.Put(name, nil); !errors.Is(err, ErrInvalidKeyName) {
			t.Errorf("Put(%q) = %v", name, err)
		}
	}
}

func TestLazyKey(t *testing.T) {
	called := false
	v := New(t.TempDir(), func() ([]byte, error) { called = true; return nil, errors.New("boom") })
	if ok, err := v.Has("secret/x"); ok || err != nil || called {
		t.Fatal("Has must not need the key")
	}
	if _, err := v.Get("secret/x"); !errors.Is(err, ErrNotFound) || called {
		t.Fatalf("Get missing must not need the key: %v", err)
	}
	if err := v.Put("secret/x", []byte("1")); err == nil {
		t.Fatal("expected key error")
	}
}

func TestBadKeyLength(t *testing.T) {
	v := New(t.TempDir(), func() ([]byte, error) { return make([]byte, 16), nil })
	if err := v.Put("secret/x", []byte("1")); err == nil {
		t.Fatal("expected error")
	}
}

func clearKeyEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvMasterKeyFile, "")
	t.Setenv(EnvMasterKeyDPAPIFile, "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	// Results must not depend on whether the tests run over SSH.
	setRemote(t, false)
}

func setRemote(t *testing.T, on bool) {
	t.Helper()
	old := RemoteSession
	RemoteSession = func() bool { return on }
	t.Cleanup(func() { RemoteSession = old })
}

func TestResolveOrder(t *testing.T) {
	keyring.MockInit()
	clearKeyEnv(t)

	// auto with nothing set generates and stores a key in the keyring.
	k1, src, err := ResolveMasterKey(BackendAuto, "")
	if err != nil || src != SourceKeyring || len(k1) != 32 {
		t.Fatalf("auto: %v %v", src, err)
	}
	k1b, _, _ := ResolveMasterKey(BackendKeyring, "")
	if !bytes.Equal(k1, k1b) {
		t.Fatal("keyring key not stable")
	}

	// systemd credential beats the keyring.
	credKey, _ := GenerateKey()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, CredentialName), []byte(EncodeKey(credKey)+"\n"), 0o600)
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	got, src, err := ResolveMasterKey(BackendAuto, "")
	if err != nil || src != SourceCredential || !bytes.Equal(got, credKey) {
		t.Fatalf("credential: %v %v", src, err)
	}

	// environment beats the credential.
	envKey, _ := GenerateKey()
	t.Setenv(EnvMasterKey, EncodeKey(envKey))
	got, src, err = ResolveMasterKey(BackendAuto, "")
	if err != nil || src != SourceEnv || !bytes.Equal(got, envKey) {
		t.Fatalf("env: %v %v", src, err)
	}
	got, src, _ = ResolveMasterKey(BackendFile, "")
	if src != SourceEnv || !bytes.Equal(got, envKey) {
		t.Fatalf("file backend: %v", src)
	}
	// keyring backend ignores env.
	got, src, _ = ResolveMasterKey(BackendKeyring, "")
	if src != SourceKeyring || !bytes.Equal(got, k1) {
		t.Fatalf("keyring backend: %v", src)
	}
}

func TestResolveErrors(t *testing.T) {
	keyring.MockInit()
	clearKeyEnv(t)

	if _, _, err := ResolveMasterKey(BackendFile, ""); !errors.Is(err, ErrNoMasterKey) {
		t.Errorf("file without key: %v", err)
	}
	if _, _, err := ResolveMasterKey(BackendKeyring, ""); !errors.Is(err, ErrNoMasterKey) {
		t.Errorf("keyring without key: %v", err)
	}
	t.Setenv(EnvMasterKey, "not base64!!")
	if _, _, err := ResolveMasterKey(BackendAuto, ""); err == nil {
		t.Error("bad env key accepted")
	}
	t.Setenv(EnvMasterKey, EncodeKey([]byte("short")))
	if _, _, err := ResolveMasterKey(BackendAuto, ""); err == nil {
		t.Error("short env key accepted")
	}
	if _, _, err := ResolveMasterKey("bogus", ""); err == nil {
		t.Error("unknown backend accepted")
	}
}

func TestKeyringUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("no dbus"))
	clearKeyEnv(t)
	_, _, err := ResolveMasterKey(BackendAuto, "")
	if !errors.Is(err, ErrNoMasterKey) {
		t.Fatalf("got %v", err)
	}
	if want := "vault init --print-key"; !bytes.Contains([]byte(err.Error()), []byte(want)) {
		t.Errorf("error lacks advice: %v", err)
	}
}

func TestInitKeyring(t *testing.T) {
	keyring.MockInit()
	if err := InitKeyring(""); err != nil {
		t.Fatal(err)
	}
	if err := InitKeyring(""); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("second init: %v", err)
	}
}

func TestAutoRefusesToOrphanExistingData(t *testing.T) {
	keyring.MockInit()
	clearKeyEnv(t)
	v, dir := testVault(t)
	if err := v.Put("secret/a", []byte("x")); err != nil {
		t.Fatal(err)
	}
	_, _, err := ResolveMasterKey(BackendAuto, dir)
	if !errors.Is(err, ErrNoMasterKey) || !strings.Contains(err.Error(), "vault reset") {
		t.Fatalf("got %v", err)
	}
	if err := InitKeyring(dir); !errors.Is(err, ErrNoMasterKey) {
		t.Fatalf("InitKeyring with data: %v", err)
	}
	if _, err := getFromKeyring(); err == nil {
		t.Fatal("a key was created")
	}

	if err := Reset(dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveMasterKey(BackendAuto, dir); err != nil {
		t.Fatalf("after reset: %v", err)
	}
}

func TestKeyringTimeout(t *testing.T) {
	clearKeyEnv(t)
	oldGet, oldTO := keyringGet, KeyringTimeout
	defer func() { keyringGet, KeyringTimeout = oldGet, oldTO }()
	block := make(chan struct{})
	defer close(block)
	keyringGet = func(string, string) (string, error) { <-block; return "", nil }
	KeyringTimeout = 50 * time.Millisecond

	_, _, err := ResolveMasterKey(BackendAuto, "")
	if !errors.Is(err, ErrNoMasterKey) || !strings.Contains(err.Error(), EnvMasterKey) {
		t.Fatalf("got %v", err)
	}
}

func TestResolveUsesStoredKeyAfterRace(t *testing.T) {
	keyring.MockInit()
	clearKeyEnv(t)
	// Another process stores its key between our miss and our Set.
	other, _ := GenerateKey()
	oldSet := keyringSet
	defer func() { keyringSet = oldSet }()
	keyringSet = func(s, u, _ string) error { return oldSet(s, u, EncodeKey(other)) }

	got, _, err := ResolveMasterKey(BackendAuto, "")
	if err != nil || !bytes.Equal(got, other) {
		t.Fatalf("must use the key that is actually stored: %v", err)
	}
}

func TestVaultLock(t *testing.T) {
	v, _ := testVault(t)
	unlock, err := v.Lock(context.Background(), "tokencache/a")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := v.Lock(context.Background(), "../x"); !errors.Is(err, ErrInvalidKeyName) {
		t.Fatalf("got %v", err)
	}
}

func TestDeleteKeepsLockFile(t *testing.T) {
	v, dir := testVault(t)
	if err := v.Put("tokencache/main", []byte("x")); err != nil {
		t.Fatal(err)
	}
	unlock, err := v.Lock(context.Background(), "tokencache/main")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := v.Delete("tokencache/main"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tokencache", "main.lock")); err != nil {
		t.Fatalf("lock file must survive Delete: %v", err)
	}
}

func TestCheckNoOrphans(t *testing.T) {
	dir := t.TempDir()
	if err := checkNoOrphans(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
	if err := checkNoOrphans(dir); err != nil {
		t.Fatalf("empty dir: %v", err)
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission-based walk error not reproducible here")
	}
	sub := filepath.Join(dir, "secret")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })
	if err := checkNoOrphans(dir); !errors.Is(err, ErrNoMasterKey) {
		t.Fatalf("unreadable vault must refuse key creation, got %v", err)
	}
}

func TestLookupMasterKeyNeverCreates(t *testing.T) {
	keyring.MockInit()
	t.Setenv(EnvMasterKey, "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if _, _, err := LookupMasterKey(BackendAuto); !errors.Is(err, ErrNoMasterKey) {
		t.Fatalf("empty keyring: %v", err)
	}
	if _, err := getFromKeyring(); err == nil {
		t.Fatal("LookupMasterKey created a key")
	}
	if _, _, err := LookupMasterKey(BackendFile); !errors.Is(err, ErrNoMasterKey) {
		t.Errorf("file backend: %v", err)
	}
	key, _ := GenerateKey()
	t.Setenv(EnvMasterKey, EncodeKey(key))
	if got, src, err := LookupMasterKey(BackendAuto); err != nil || src != SourceEnv || !bytes.Equal(got, key) {
		t.Errorf("env: %v %v", src, err)
	}
	t.Setenv(EnvMasterKey, "")
	if err := InitKeyring(""); err != nil {
		t.Fatal(err)
	}
	if _, src, err := LookupMasterKey(BackendKeyring); err != nil || src != SourceKeyring {
		t.Errorf("keyring: %v %v", src, err)
	}
	keyring.MockInitWithError(errors.New("no dbus"))
	if _, _, err := LookupMasterKey(BackendAuto); !errors.Is(err, ErrNoMasterKey) {
		t.Errorf("unavailable keyring: %v", err)
	}
	if _, _, err := LookupMasterKey("cloud"); err == nil {
		t.Error("unknown backend accepted")
	}
}

func TestMasterKeyFromFile(t *testing.T) {
	keyring.MockInit()
	t.Setenv(EnvMasterKey, "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	key, _ := GenerateKey()
	p := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(p, []byte(EncodeKey(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMasterKeyFile, p)
	got, src, err := ResolveMasterKey(BackendFile, "")
	if err != nil || src != SourceKeyFile || !bytes.Equal(got, key) {
		t.Fatalf("file key: %v %v", src, err)
	}
	if got, _, err := LookupMasterKey(BackendAuto); err != nil || !bytes.Equal(got, key) {
		t.Errorf("lookup: %v", err)
	}
	// The environment variable still wins.
	other, _ := GenerateKey()
	t.Setenv(EnvMasterKey, EncodeKey(other))
	if got, src, _ := ResolveMasterKey(BackendAuto, ""); src != SourceEnv || !bytes.Equal(got, other) {
		t.Errorf("env must win: %v", src)
	}
	t.Setenv(EnvMasterKey, "")
	// A missing or damaged file is an error, never a silent fallback to a new key.
	t.Setenv(EnvMasterKeyFile, filepath.Join(t.TempDir(), "gone"))
	if _, _, err := ResolveMasterKey(BackendAuto, ""); err == nil {
		t.Error("missing key file ignored")
	}
	if err := os.WriteFile(p, []byte("not base64!"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMasterKeyFile, p)
	if _, _, err := ResolveMasterKey(BackendAuto, ""); err == nil {
		t.Error("damaged key file ignored")
	}
}

// Over SSH the keyring may only be out of reach: never create a key there,
// and never send the user towards wiping the vault.
func TestRemoteSessionNeverCreatesAKey(t *testing.T) {
	keyring.MockInit()
	clearKeyEnv(t)
	setRemote(t, true)
	dir := t.TempDir()
	_, _, err := ResolveMasterKey(BackendAuto, dir)
	if !errors.Is(err, ErrNoMasterKey) || !errors.Is(err, ErrRemoteSession) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "vault reset") {
		t.Errorf("remote session error suggests a reset: %v", err)
	}
	if _, err := keyring.Get(keyringService, keyringUser); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("a key was created from a remote session (err=%v)", err)
	}
	if _, _, err := LookupMasterKey(BackendAuto); !errors.Is(err, ErrRemoteSession) {
		t.Errorf("lookup: got %v", err)
	}
	// The environment still works remotely.
	key, _ := GenerateKey()
	t.Setenv(EnvMasterKey, EncodeKey(key))
	if _, src, err := ResolveMasterKey(BackendAuto, dir); err != nil || src != SourceEnv {
		t.Errorf("env key over SSH: src=%v err=%v", src, err)
	}
}

func TestListNeedsNoMasterKeyAndSkipsNonEntries(t *testing.T) {
	v, dir := testVault(t)
	for _, k := range []string{"secret/main", "tokencache/main", "notify/webhook", "secret/other"} {
		if err := v.Put(k, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	// A lock file, an interrupted atomic write and a stray file are not entries.
	for _, junk := range []string{"secret/main.lock", "secret/other.bin.tmp-123", "notes.txt", "secret/bad name.bin"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(junk)), []byte("junk"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// A vault whose key lookup always fails still lists, because it never asks for the key.
	keyless := New(dir, func() ([]byte, error) { return nil, ErrNoMasterKey })
	got, err := keyless.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"notify/webhook", "secret/main", "secret/other", "tokencache/main"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("List = %v, want %v", got, want)
	}
}

func TestListMissingDirIsEmpty(t *testing.T) {
	v := New(filepath.Join(t.TempDir(), "nope"), func() ([]byte, error) { return nil, ErrNoMasterKey })
	got, err := v.List()
	if err != nil || len(got) != 0 {
		t.Fatalf("List = %v, %v", got, err)
	}
}

func TestSnapshotAndRestore(t *testing.T) {
	key, _ := GenerateKey()
	v := New(t.TempDir(), func() ([]byte, error) { return key, nil })
	other, _ := GenerateKey()
	// Entries that cannot be decrypted are snapshotted and restored byte for byte.
	foreign := New(v.dir, func() ([]byte, error) { return other, nil })
	if err := foreign.Put("secret/a", []byte("old-a")); err != nil {
		t.Fatal(err)
	}
	rawA, hadA, err := v.Snapshot("secret/a")
	if err != nil || !hadA {
		t.Fatal(err, hadA)
	}
	_, hadB, err := v.Snapshot("secret/b")
	if err != nil || hadB {
		t.Fatal("a missing entry reported as present", err)
	}
	if err := v.Put("secret/a", []byte("new-a")); err != nil {
		t.Fatal(err)
	}
	if err := v.Put("secret/b", []byte("new-b")); err != nil {
		t.Fatal(err)
	}
	if err := v.Restore("secret/a", rawA, true); err != nil {
		t.Fatal(err)
	}
	if err := v.Restore("secret/b", nil, false); err != nil {
		t.Fatal(err)
	}
	if got, err := foreign.Get("secret/a"); err != nil || string(got) != "old-a" {
		t.Fatalf("a: %q %v", got, err)
	}
	if ok, _ := v.Has("secret/b"); ok {
		t.Error("b was not removed")
	}
	if err := v.Restore("secret/b", nil, false); err != nil {
		t.Errorf("restoring an absent entry twice: %v", err)
	}
}
