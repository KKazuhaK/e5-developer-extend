//go:build linux

package sdnotify

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func listen(t *testing.T, name string) *net.UnixConn {
	t.Helper()
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		t.Skipf("unixgram sockets unavailable: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func read(t *testing.T, c *net.UnixConn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 128)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf[:n])
}

func TestMessagesOverPathSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "notify.sock")
	c := listen(t, sock)
	t.Setenv("NOTIFY_SOCKET", sock)
	for _, tc := range []struct {
		send func() error
		want string
	}{{Ready, "READY=1"}, {Watchdog, "WATCHDOG=1"}, {Stopping, "STOPPING=1"}} {
		if err := tc.send(); err != nil {
			t.Fatal(err)
		}
		if got := read(t, c); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestAbstractSocket(t *testing.T) {
	name := fmt.Sprintf("e5renew-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	c := listen(t, "\x00"+name)
	t.Setenv("NOTIFY_SOCKET", "@"+name)
	if err := Ready(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c); got != "READY=1" {
		t.Errorf("got %q", got)
	}
}

func TestNoSocketIsNoop(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := Ready(); err != nil {
		t.Errorf("unset NOTIFY_SOCKET: %v", err)
	}
	t.Setenv("NOTIFY_SOCKET", "vsock:1:2")
	if err := Ready(); err != nil {
		t.Errorf("unsupported address: %v", err)
	}
	// A socket nobody listens on is an error the caller may log.
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "gone"))
	if err := Ready(); err == nil {
		t.Error("missing socket: want an error")
	}
}
