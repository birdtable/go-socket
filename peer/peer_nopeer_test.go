//go:build linux || darwin || freebsd

package peer

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// A unix socket the kernel recorded no peer for is never a peer: a datagram
// socket, bound or connected, and a stream socket that never connected. On
// Linux the kernel answers SO_PEERCRED for each with the uid 4294967295, and
// PeerOf refuses it by name (refusedAsNoPeer); macOS refuses each itself.
//
// What it costs when this fails: a program that calls PeerOf on such a socket
// reads the uid 4294967295 as a user, and trusts it if it trusts that uid.
func TestPeerOfRefusesAUnixSocketWithNoPeer(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("/tmp", "peer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	addr := &net.UnixAddr{Name: filepath.Join(dir, "g.sock"), Net: "unixgram"}
	bound, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bound.Close() })
	dialed, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dialed.Close() })

	for name, c := range map[string]net.Conn{
		"a bound datagram socket":         bound,
		"a connected datagram socket":     dialed,
		"a stream socket never connected": unconnectedStream(t),
	} {
		p, err := PeerOf(c)
		if err == nil || p != (Peer{}) {
			t.Errorf("%s: %+v %v, want an error and no peer", name, p, err)
		}
		refusedAsNoPeer(t, name, err)
	}
}

// unconnectedStream is a unix stream socket that never connected, as a
// net.Conn.
func unconnectedStream(t *testing.T) net.Conn {
	t.Helper()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fd), "unconnected")
	defer func() { _ = f.Close() }()
	c, err := net.FileConn(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
