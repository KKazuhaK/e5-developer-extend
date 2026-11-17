//go:build !windows

package vault

import "os"

// Elsewhere there is no DPAPI; the hooks only exist so the logic in
// keyfile.go can be tested with fakes.
func platformKeyFileOS() keyFileOS {
	return keyFileOS{
		protect:   func([]byte) ([]byte, error) { return nil, ErrDPAPIUnsupported },
		unprotect: func([]byte) ([]byte, error) { return nil, ErrDPAPIUnsupported },
		create: func(path string) (*os.File, error) {
			return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		},
		readACL: func(string) (ACLInfo, error) { return ACLInfo{}, ErrDPAPIUnsupported },
	}
}
