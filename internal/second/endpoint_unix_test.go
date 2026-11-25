//go:build linux || darwin || freebsd

package second_test

import (
	"context"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/birdtable/go-socket/listen"
	"github.com/birdtable/go-socket/peer"
)

// endpoint is the shop's socket: group-readable (0640, not our 0600), in the
// process's own group, behind a peer check that lets this user alone in.
func endpoint(t *testing.T, ctx context.Context) (*listen.Listener, func(context.Context) (net.Conn, error)) {
	t.Helper()
	dir, err := os.MkdirTemp("", "fernhill")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "press.sock")
	opts := []listen.ListenOption{listen.SocketMode(0o640)}
	if g, err := user.LookupGroupId(strconv.Itoa(os.Getgid())); err == nil {
		opts = append(opts, listen.SocketGroup(g.Name))
	} else {
		t.Logf("the process's group has no name here (%v), so the socket keeps its group", err)
	}
	l, err := listen.Listen(ctx, sock, opts...)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("the shop's socket is not 0640: %v %v", fi.Mode(), err)
	}
	l.Listener = peer.Listener(l.Listener, peer.Principals(peer.Unix(uint32(os.Getuid()))), nil)
	return l, func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}
}
