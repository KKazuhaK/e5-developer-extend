//go:build windows

package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsDPAPIRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), DPAPIKeyFileName)
	key := testKey()
	if err := WriteDPAPIKeyFile(path, key); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, key) {
		t.Fatal("the plain key is in the file")
	}
	got, err := ReadDPAPIKeyFile(path)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("read back: %v", err)
	}
	// Machine scope: the same blob decrypts without the CRYPTPROTECT_LOCAL_MACHINE
	// flag on the way back (the flag lives in the blob), and nothing else is needed.
	t.Setenv(EnvMasterKeyDPAPIFile, path)
	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvMasterKeyFile, "")
	if k, src, err := LookupMasterKey(BackendAuto); err != nil || src != SourceDPAPIFile || !bytes.Equal(k, key) {
		t.Fatalf("lookup: %v %s", err, src)
	}
}

func TestWindowsKeyFileACLIsRestricted(t *testing.T) {
	path := filepath.Join(t.TempDir(), DPAPIKeyFileName)
	if err := WriteDPAPIKeyFile(path, testKey()); err != nil {
		t.Fatal(err)
	}
	info, err := readACL(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Protected {
		t.Error("the DACL still inherits from the folder")
	}
	for _, e := range info.ACEs {
		if e.Inherited {
			t.Errorf("inherited entry left: %+v", e)
		}
	}
	if len(info.ACEs) != 3 {
		t.Errorf("want user, SYSTEM and Administrators, got %+v", info.ACEs)
	}
	if a := AuditDPAPIKeyFile(path); a.Missing || a.ReadErr != "" || len(a.Broad) != 0 {
		t.Errorf("audit of a fresh file: %+v", a)
	}
}

func TestWindowsAuditDetectsWiderACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), DPAPIKeyFileName)
	if err := WriteDPAPIKeyFile(path, testKey()); err != nil {
		t.Fatal(err)
	}
	// Let Everyone read it (and turn inheritance back on, as a careless
	// "reset permissions" would).
	sd, err := windows.SecurityDescriptorFromString("D:(A;;FR;;;WD)(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Skipf("cannot change the ACL here: %v", err)
	}
	a := AuditDPAPIKeyFile(path)
	found := false
	for _, p := range a.Broad {
		if p == "Everyone" {
			found = true
		}
	}
	if !found {
		t.Errorf("Everyone not reported: %+v", a)
	}
}

func TestWindowsRewriteKeepsRestrictedACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), DPAPIKeyFileName)
	for i := 0; i < 2; i++ {
		if err := WriteDPAPIKeyFile(path, testKey()); err != nil {
			t.Fatal(err)
		}
	}
	if a := AuditDPAPIKeyFile(path); len(a.Broad) != 0 || a.ReadErr != "" {
		t.Errorf("audit after a rewrite: %+v", a)
	}
}
