package vault

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The Windows headless key file: the master key encrypted with DPAPI at
// machine scope (CryptProtectData with CRYPTPROTECT_LOCAL_MACHINE) and kept
// next to the configuration. Machine scope is what lets a scheduled task that
// runs without a Windows password (logon type S4U, no user profile keys
// loaded) decrypt it; the price is that any process on the same machine can
// decrypt the blob, so the file's NTFS ACL (user, SYSTEM and Administrators
// only, no inherited entries) is the actual protection.

const (
	// EnvMasterKeyDPAPIFile names a DPAPI machine-scope key file. The
	// Windows scheduled task passes it as --dpapi-key-file, because a task
	// action cannot set environment variables.
	EnvMasterKeyDPAPIFile = "E5EXTEND_MASTER_KEY_DPAPI_FILE"
	// DPAPIKeyFileName is the file written by `service install --headless`.
	DPAPIKeyFileName = "master.key.dpapi"

	dpapiHeader = "E5EXTEND-DPAPI-KEY v1 machine"
)

// dpapiEntropy is mixed into the encryption. It is not a secret (it is in
// the source); it only stops generic "decrypt every DPAPI blob" tools from
// working on this file without knowing what it is.
//
//lint:ignore U1000 only the Windows build reads it
var dpapiEntropy = []byte("e5extend master key v1")

// ErrDPAPIUnsupported means this platform has no DPAPI.
var ErrDPAPIUnsupported = errors.New("vault: DPAPI key files only work on Windows")

// Well-known SIDs that may always have access to the key file.
const (
	sidSystem         = "S-1-5-18"
	sidAdministrators = "S-1-5-32-544"
)

// ACE is one access-control entry of a file, reduced to what the audit needs.
type ACE struct {
	SID       string // S-1-5-...
	Name      string // DOMAIN\name, or the SID when it cannot be resolved
	Allow     bool   // an allow entry (deny entries never widen access)
	Inherited bool
	// InheritOnly entries do not apply to the object itself, only to what is
	// created inside it (CREATOR OWNER is the usual one).
	InheritOnly bool
	Mask        uint32 // access rights granted
}

// ACLInfo is the access list of a file.
type ACLInfo struct {
	UserSID   string // the account the process runs as
	Protected bool   // inheritance from the parent folder is switched off
	NoDACL    bool   // no access list at all: everyone has full access
	ACEs      []ACE
}

// keyFileOS is what differs per platform; tests replace it.
type keyFileOS struct {
	protect   func(plain []byte) ([]byte, error)
	unprotect func(blob []byte) ([]byte, error)
	// create makes a new empty file that only the user, SYSTEM and
	// Administrators can open, before any content is written to it.
	create  func(path string) (*os.File, error)
	readACL func(path string) (ACLInfo, error)
	// mkdir creates one directory that only the user, SYSTEM and
	// Administrators can use (children inherit that).
	mkdir func(path string) error
}

var keyFileOps = platformKeyFileOS()

// WriteDPAPIKeyFile stores key in path, encrypted with DPAPI at machine scope
// and readable only by the user, SYSTEM and Administrators. An existing file
// is replaced atomically; the new file has its restricted ACL before it holds
// any data.
func WriteDPAPIKeyFile(path string, key []byte) error {
	if len(key) != keySize {
		return fmt.Errorf("vault: master key must be %d bytes, got %d", keySize, len(key))
	}
	blob, err := keyFileOps.protect(key)
	if err != nil {
		return fmt.Errorf("vault: protect the master key with DPAPI: %w", err)
	}
	data := []byte(dpapiHeader + "\n" + base64.StdEncoding.EncodeToString(blob) + "\n")

	dir := filepath.Dir(path)
	if err := mkdirRestricted(dir); err != nil {
		return fmt.Errorf("vault: create %s: %w", dir, err)
	}
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := path + ".tmp-" + hex.EncodeToString(rnd[:])
	f, err := keyFileOps.create(tmp)
	if err != nil {
		return fmt.Errorf("vault: create the key file: %w", err)
	}
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("vault: write the key file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("vault: write the key file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("vault: write the key file: %w", err)
	}
	return nil
}

// mkdirRestricted creates dir and any missing parents. Directories it creates
// get the restricted access list (on Windows); one that already exists is left
// as it is, and AuditDPAPIKeyFile reports it when others can write to it.
func mkdirRestricted(dir string) error {
	var missing []string
	for d := dir; ; {
		if _, err := os.Stat(d); err == nil {
			break
		}
		missing = append(missing, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := keyFileOps.mkdir(missing[i]); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return nil
}

// ReadDPAPIKeyFile returns the master key stored by WriteDPAPIKeyFile.
func ReadDPAPIKeyFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	head, body, ok := strings.Cut(string(data), "\n")
	if !ok || strings.TrimSpace(head) != dpapiHeader {
		return nil, errors.New("not an e5extend DPAPI key file (unknown header)")
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body))
	if err != nil || len(blob) == 0 {
		return nil, errors.New("the DPAPI key file is damaged (bad base64)")
	}
	key, err := keyFileOps.unprotect(blob)
	if err != nil {
		return nil, fmt.Errorf("cannot decrypt with DPAPI (a file copied from another machine cannot be read here): %w", err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("the DPAPI key file holds %d bytes, want %d", len(key), keySize)
	}
	return key, nil
}

// KeyFileAudit is what AuditDPAPIKeyFile found.
type KeyFileAudit struct {
	Missing bool
	// ReadErr says why an existing file cannot be read or decrypted; it never
	// contains the file's path.
	ReadErr string
	// Broad lists the accounts, other than the user, SYSTEM and
	// Administrators, that the ACL grants access to.
	Broad []string
	// ACLErr says why the ACL could not be read (the check is then skipped).
	ACLErr string
	// Dir is the folder holding the key file; DirBroad lists the accounts,
	// other than the user, SYSTEM and Administrators, that can write to it,
	// delete from it or change its permissions (they could swap the key file).
	Dir      string
	DirBroad []string
}

// AuditDPAPIKeyFile checks that the key file exists, can be decrypted by this
// process, and is not accessible to anyone beyond the user, SYSTEM and
// Administrators. It never returns the key.
func AuditDPAPIKeyFile(path string) KeyFileAudit {
	var a KeyFileAudit
	if _, err := ReadDPAPIKeyFile(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			a.Missing = true
			return a
		}
		a.ReadErr = pathless(err)
	}
	info, err := keyFileOps.readACL(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			a.ACLErr = pathless(err)
		}
		return a
	}
	a.Broad = BroadPrincipals(info)
	a.Dir = filepath.Dir(path)
	if dinfo, err := keyFileOps.readACL(a.Dir); err == nil {
		a.DirBroad = WritePrincipals(dinfo)
	}
	return a
}

// pathless drops the file name from an *fs.PathError so a message can be
// shown without the user's directory in it.
func pathless(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// BroadPrincipals lists who, besides the user, SYSTEM and Administrators, an
// ACL lets in. A file without any access list is open to everyone.
func BroadPrincipals(info ACLInfo) []string {
	if info.NoDACL {
		return []string{"Everyone (the file has no access list)"}
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range info.ACEs {
		if !e.Allow || e.InheritOnly || e.SID == sidSystem || e.SID == sidAdministrators || (info.UserSID != "" && e.SID == info.UserSID) {
			continue
		}
		name := e.Name
		if name == "" {
			name = e.SID
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Rights that let an account change a folder's content or permissions.
const (
	rightAddFile      = 0x2     // FILE_ADD_FILE / FILE_WRITE_DATA
	rightAddSubdir    = 0x4     // FILE_ADD_SUBDIRECTORY / FILE_APPEND_DATA
	rightDeleteChild  = 0x40    // FILE_DELETE_CHILD
	rightDelete       = 0x10000 // DELETE
	rightWriteDAC     = 0x40000
	rightWriteOwner   = 0x80000
	rightGenericAll   = 0x10000000
	rightGenericWrite = 0x40000000
	writeRights       = rightAddFile | rightAddSubdir | rightDeleteChild | rightDelete | rightWriteDAC | rightWriteOwner | rightGenericAll | rightGenericWrite
)

// WritePrincipals lists who, besides the user, SYSTEM and Administrators,
// can add to, delete from or re-permission the object an ACL belongs to.
func WritePrincipals(info ACLInfo) []string {
	if info.NoDACL {
		return []string{"Everyone (no access list)"}
	}
	narrowed := info
	narrowed.ACEs = nil
	for _, e := range info.ACEs {
		if e.Mask&writeRights != 0 {
			narrowed.ACEs = append(narrowed.ACEs, e)
		}
	}
	return BroadPrincipals(narrowed)
}
