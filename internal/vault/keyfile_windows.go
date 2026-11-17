//go:build windows

package vault

import (
	"fmt"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformKeyFileOS() keyFileOS {
	return keyFileOS{protect: dpapiProtect, unprotect: dpapiUnprotect, create: createRestricted, readACL: readACL, mkdir: mkdirRestrictedOS}
}

func blob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

// copyOut copies a DPAPI output blob and frees it.
func copyOut(out *windows.DataBlob) []byte {
	if out.Data == nil {
		return nil
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...)
}

func dpapiProtect(plain []byte) ([]byte, error) {
	var out windows.DataBlob
	// CRYPTPROTECT_LOCAL_MACHINE: any account on this machine can decrypt, so
	// the S4U task works without the user's profile; the file ACL guards it.
	err := windows.CryptProtectData(blob(plain), windows.StringToUTF16Ptr("e5extend master key"), blob(dpapiEntropy), 0, nil,
		windows.CRYPTPROTECT_UI_FORBIDDEN|windows.CRYPTPROTECT_LOCAL_MACHINE, &out)
	if err != nil {
		return nil, err
	}
	return copyOut(&out), nil
}

func dpapiUnprotect(b []byte) ([]byte, error) {
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(blob(b), nil, blob(dpapiEntropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	return copyOut(&out), nil
}

func currentUserSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid, nil
}

// restrictedAttributes returns SECURITY_ATTRIBUTES whose descriptor holds a
// protected DACL (no inheritance from the parent): full control for SYSTEM,
// Administrators and the current user. Passing it to CreateFile or
// CreateDirectory means the object never exists with another access list.
// The user's SID is explicit because under UAC the Administrators group in a
// limited token is deny-only. For a directory the entries are inherited by
// what is created inside it.
func restrictedAttributes(dir bool) (*windows.SecurityAttributes, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, fmt.Errorf("find the current user: %w", err)
	}
	flags := ""
	if dir {
		flags = "OICI"
	}
	// D:P = DACL, protected; FA = full access; SY = SYSTEM, BA = Administrators.
	sddl := "D:P(A;" + flags + ";FA;;;SY)(A;" + flags + ";FA;;;BA)(A;" + flags + ";FA;;;" + sid.String() + ")"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, nil
}

// createRestricted creates a new file that is born with the restricted access
// list, opened for writing with no sharing, so nobody can open it between its
// creation and the rename that puts it in place.
func createRestricted(path string) (*os.File, error) {
	sa, err := restrictedAttributes(false)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "create", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// mkdirRestrictedOS creates one directory with the restricted access list.
func mkdirRestrictedOS(path string) error {
	sa, err := restrictedAttributes(true)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(name, sa); err != nil {
		if err == windows.ERROR_ALREADY_EXISTS {
			return fs.ErrExist
		}
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

func readACL(path string) (ACLInfo, error) {
	var info ACLInfo
	if sid, err := currentUserSID(); err == nil {
		info.UserSID = sid.String()
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return info, err
	}
	if ctl, _, err := sd.Control(); err == nil {
		info.Protected = ctl&windows.SE_DACL_PROTECTED != 0
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		info.NoDACL = true
		return info, nil
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return info, err
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		e := ACE{
			SID:       sid.String(),
			Allow:     ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE,
			Inherited: ace.Header.AceFlags&windows.INHERITED_ACE != 0,
			// INHERIT_ONLY_ACE (0x08): applies to children only.
			InheritOnly: ace.Header.AceFlags&0x08 != 0,
			Mask:        uint32(ace.Mask),
		}
		e.Name = e.SID
		if acc, dom, _, err := sid.LookupAccount(""); err == nil {
			if dom != "" {
				e.Name = dom + `\` + acc
			} else {
				e.Name = acc
			}
		}
		info.ACEs = append(info.ACEs, e)
	}
	return info, nil
}
