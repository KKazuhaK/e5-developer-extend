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

// The file must have its final access list from the moment it exists, before
// anything is written to it (no create-then-restrict window).
func TestWindowsCreatedFileHasTheACLBeforeAnyWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.dpapi")
	f, err := createRestricted(path)
	if err != nil {
		t.Fatal(err)
	}
	// Still empty: nothing has been written.
	if fi, _ := os.Stat(path); fi == nil || fi.Size() != 0 {
		t.Fatalf("not empty: %v", fi)
	}
	// Opening it for data while the creator holds it with no sharing fails.
	if g, err := os.Open(path); err == nil {
		g.Close()
		t.Error("the new file was shareable")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := readACL(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Protected || len(info.ACEs) != 3 {
		t.Fatalf("acl = %+v", info)
	}
	for _, e := range info.ACEs {
		if e.Inherited || !e.Allow {
			t.Errorf("entry %+v", e)
		}
	}
	if got := BroadPrincipals(info); len(got) != 0 {
		t.Errorf("broad = %v", got)
	}
}

func TestWindowsCreatedFolderIsRestrictedAndInherited(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cfg", DPAPIKeyFileName)
	if err := WriteDPAPIKeyFile(path, testKey()); err != nil {
		t.Fatal(err)
	}
	info, err := readACL(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Protected || len(info.ACEs) != 3 {
		t.Fatalf("folder acl = %+v", info)
	}
	if a := AuditDPAPIKeyFile(path); len(a.DirBroad) != 0 || len(a.Broad) != 0 || a.ReadErr != "" {
		t.Errorf("audit = %+v", a)
	}
}
