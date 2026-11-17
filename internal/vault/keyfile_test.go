package vault

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDPAPI replaces the platform hooks: "encryption" is a reversible XOR, so
// the file format, the atomic write, the audit and the environment source can
// be tested anywhere. The real DPAPI and ACL calls are tested on Windows
// (keyfile_windows_test.go).
func fakeDPAPI(t *testing.T) *ACLInfo {
	t.Helper()
	acl := &ACLInfo{UserSID: "S-1-5-21-1-2-3-1001", Protected: true, ACEs: []ACE{
		{SID: "S-1-5-18", Name: `NT AUTHORITY\SYSTEM`, Allow: true},
		{SID: "S-1-5-32-544", Name: `BUILTIN\Administrators`, Allow: true},
		{SID: "S-1-5-21-1-2-3-1001", Name: `PC\alice`, Allow: true},
	}}
	old := keyFileOps
	t.Cleanup(func() { keyFileOps = old })
	xor := func(b []byte) []byte {
		out := make([]byte, len(b))
		for i, c := range b {
			out[i] = c ^ 0x5a
		}
		return out
	}
	keyFileOps = keyFileOS{
		protect:   func(p []byte) ([]byte, error) { return xor(p), nil },
		unprotect: func(b []byte) ([]byte, error) { return xor(b), nil },
		create: func(path string) (*os.File, error) {
			return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		},
		readACL: func(string) (ACLInfo, error) { return *acl, nil },
	}
	return acl
}

func testKey() []byte {
	k := make([]byte, keySize)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func TestDPAPIKeyFileRoundTrip(t *testing.T) {
	fakeDPAPI(t)
	path := filepath.Join(t.TempDir(), "cfg", DPAPIKeyFileName)
	key := testKey()
	if err := WriteDPAPIKeyFile(path, key); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(raw), dpapiHeader+"\n") {
		t.Errorf("header missing: %q", raw)
	}
	if bytes.Contains(raw, key) || strings.Contains(string(raw), EncodeKey(key)) {
		t.Error("the plain key is in the file")
	}
	got, err := ReadDPAPIKeyFile(path)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("read back: %v", err)
	}
	// Rewriting replaces the file and leaves no temporary files behind.
	other := bytes.Repeat([]byte{9}, keySize)
	if err := WriteDPAPIKeyFile(path, other); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadDPAPIKeyFile(path); !bytes.Equal(got, other) {
		t.Error("not replaced")
	}
	ents, _ := os.ReadDir(filepath.Dir(path))
	if len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
	if err := WriteDPAPIKeyFile(path, []byte("short")); err == nil {
		t.Error("a short key was accepted")
	}
}

func TestDPAPIKeyFileErrors(t *testing.T) {
	fakeDPAPI(t)
	dir := t.TempDir()
	if _, err := ReadDPAPIKeyFile(filepath.Join(dir, "none")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	for name, body := range map[string]string{
		"header": "hello\nAAAA\n",
		"base64": dpapiHeader + "\n!!!\n",
		"empty":  dpapiHeader + "\n",
		"length": dpapiHeader + "\nAAAA\n",
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDPAPIKeyFile(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A blob from another machine cannot be decrypted.
	keyFileOps.unprotect = func([]byte) ([]byte, error) { return nil, errors.New("Key not valid for use in specified state.") }
	p := filepath.Join(dir, "ok")
	_ = os.WriteFile(p, []byte(dpapiHeader+"\nQUJD\n"), 0o600)
	if _, err := ReadDPAPIKeyFile(p); err == nil || !strings.Contains(err.Error(), "another machine") {
		t.Errorf("foreign blob: %v", err)
	}
}

func TestAuditDPAPIKeyFile(t *testing.T) {
	acl := fakeDPAPI(t)
	path := filepath.Join(t.TempDir(), DPAPIKeyFileName)
	if a := AuditDPAPIKeyFile(path); !a.Missing {
		t.Errorf("missing file: %+v", a)
	}
	if err := WriteDPAPIKeyFile(path, testKey()); err != nil {
		t.Fatal(err)
	}
	if a := AuditDPAPIKeyFile(path); a.Missing || a.ReadErr != "" || len(a.Broad) != 0 || a.ACLErr != "" {
		t.Errorf("clean file: %+v", a)
	}
	acl.ACEs = append(acl.ACEs,
		ACE{SID: "S-1-5-32-545", Name: `BUILTIN\Users`, Allow: true, Inherited: true},
		ACE{SID: "S-1-1-0", Name: "Everyone", Allow: true},
		ACE{SID: "S-1-5-32-546", Name: `BUILTIN\Guests`, Allow: false}) // a deny never widens access
	a := AuditDPAPIKeyFile(path)
	if strings.Join(a.Broad, ",") != `BUILTIN\Users,Everyone` {
		t.Errorf("broad = %v", a.Broad)
	}
	// An unreadable file reports why, without its path.
	keyFileOps.unprotect = func([]byte) ([]byte, error) { return nil, errors.New("bad state") }
	a = AuditDPAPIKeyFile(path)
	if a.ReadErr == "" || strings.Contains(a.ReadErr, path) {
		t.Errorf("read error = %q", a.ReadErr)
	}
	// An ACL that cannot be read is reported, not mistaken for "fine".
	keyFileOps.readACL = func(string) (ACLInfo, error) { return ACLInfo{}, errors.New("no access") }
	if a = AuditDPAPIKeyFile(path); a.ACLErr == "" {
		t.Errorf("acl error lost: %+v", a)
	}
}

func TestBroadPrincipals(t *testing.T) {
	if got := BroadPrincipals(ACLInfo{NoDACL: true}); len(got) != 1 || !strings.Contains(got[0], "Everyone") {
		t.Errorf("no DACL: %v", got)
	}
	got := BroadPrincipals(ACLInfo{UserSID: "S-1-5-21-9", ACEs: []ACE{
		{SID: "S-1-5-21-9", Name: "me", Allow: true},
		{SID: "S-1-5-21-10", Allow: true}, // unresolved name: the SID is shown
		{SID: "S-1-5-21-10", Allow: true}, // listed once
	}})
	if len(got) != 1 || got[0] != "S-1-5-21-10" {
		t.Errorf("got %v", got)
	}
}

func TestResolveReadsDPAPIKeyFile(t *testing.T) {
	clearKeyEnv(t)
	fakeDPAPI(t)
	t.Setenv(EnvMasterKeyDPAPIFile, "")
	path := filepath.Join(t.TempDir(), DPAPIKeyFileName)
	key := testKey()
	if err := WriteDPAPIKeyFile(path, key); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMasterKeyDPAPIFile, path)
	for _, backend := range []string{BackendAuto, BackendFile} {
		got, src, err := ResolveMasterKey(backend, t.TempDir())
		if err != nil || src != SourceDPAPIFile || !bytes.Equal(got, key) {
			t.Fatalf("%s: %v %s", backend, err, src)
		}
	}
	got, src, err := LookupMasterKey(BackendAuto)
	if err != nil || src != SourceDPAPIFile || !bytes.Equal(got, key) {
		t.Fatalf("lookup: %v %s", err, src)
	}
	// An explicit environment key still wins, like the other file sources.
	other := bytes.Repeat([]byte{7}, keySize)
	t.Setenv(EnvMasterKey, EncodeKey(other))
	if got, src, _ := ResolveMasterKey(BackendAuto, ""); src != SourceEnv || !bytes.Equal(got, other) {
		t.Errorf("env did not win: %s", src)
	}
}

func TestResolveDPAPIKeyFileErrorHasNoPathOrKey(t *testing.T) {
	clearKeyEnv(t)
	fakeDPAPI(t)
	p := filepath.Join(t.TempDir(), "secret-user-dir", DPAPIKeyFileName)
	t.Setenv(EnvMasterKeyDPAPIFile, p)
	_, _, err := ResolveMasterKey(BackendAuto, "")
	if err == nil {
		t.Fatal("a missing key file was accepted")
	}
	if strings.Contains(err.Error(), "secret-user-dir") {
		t.Errorf("the path leaked: %v", err)
	}
	if !strings.Contains(err.Error(), EnvMasterKeyDPAPIFile) {
		t.Errorf("the message does not name the variable: %v", err)
	}
}

func TestDPAPIUnsupportedOffWindows(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows has DPAPI")
	}
	if err := WriteDPAPIKeyFile(filepath.Join(t.TempDir(), "k"), testKey()); !errors.Is(err, ErrDPAPIUnsupported) {
		t.Errorf("err = %v", err)
	}
}
