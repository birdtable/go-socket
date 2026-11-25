//go:build unix

package peer

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The helpers here serve the peer tests, each of which runs on Unix alone
// (peer_known_test.go, listener_test.go, peer_pid_*_test.go,
// peer_nonlinux_unix_test.go), so they build there too.

// maxSocketPath is the most bytes a test's socket path may have: macOS holds a
// unix socket's address to 104, and Linux to 108.
const maxSocketPath = 100

// me is the user these tests run as: the peer of every unix connection they
// make.
func me() uint32 { return uint32(os.Geteuid()) }

// unixPeer is the server's end of a unix socket this process connected to,
// in a folder under /tmp: on macOS t.TempDir is too long a path for a
// socket's address.
func unixPeer(t *testing.T) net.Conn {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "peer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	if len(path) > maxSocketPath {
		t.Fatalf("the socket path %q is %d bytes, over the %d a unix socket's address may hold", path, len(path), maxSocketPath)
	}
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	var d net.Dialer
	c, err := d.DialContext(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	s, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
