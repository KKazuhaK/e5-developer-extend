//go:build linux

package sdnotify

import (
	"net"
	"os"
	"strings"
	"time"
)

// Notify sends state (for example "READY=1") to the socket named by
// NOTIFY_SOCKET. It does nothing and returns nil when the variable is unset.
func Notify(state string) error {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil
	}
	// Only filesystem paths and the abstract namespace ("@name") are valid.
	if !strings.HasPrefix(addr, "/") && !strings.HasPrefix(addr, "@") {
		return nil
	}
	if addr[0] == '@' {
		addr = "\x00" + addr[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	// A datagram socket does not block on a slow reader for long, but never wait forever.
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err = conn.Write([]byte(state))
	return err
}
