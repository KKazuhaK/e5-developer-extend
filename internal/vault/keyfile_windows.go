//go:build windows

package vault

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformKeyFileOS() keyFileOS {
	return keyFileOS{protect: dpapiProtect, unprotect: dpapiUnprotect, create: createRestricted, readACL: readACL}
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

// createRestricted creates a new file and, while it is still empty, replaces
// its access list with: full control for the current user, SYSTEM and
// Administrators, and nothing inherited from the folder.
func createRestricted(path string) (*os.File, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, fmt.Errorf("find the current user: %w", err)
	}
	// D:P = DACL, protected (no inheritance); FA = file all access;
	// SY = SYSTEM, BA = Administrators.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + sid.String() + ")")
	if err != nil {
		return nil, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("restrict access to the key file: %w", err)
	}
	return f, nil
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
