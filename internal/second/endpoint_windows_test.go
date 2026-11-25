//go:build windows

package second_test

import (
	"context"
	"crypto/rand"
	"net"
	"os/user"
	"testing"

	"github.com/birdtable/go-socket/listen"
	"github.com/birdtable/go-socket/peer"
)

// endpoint is the shop's named pipe, which listen.Listen makes for this
// account and SYSTEM alone, behind a peer check that lets this account
// alone in: peer reads the pipe's client account, by its SID, before a byte
// of the request, from a client that dials with peer.DialPipe.
func endpoint(t *testing.T, ctx context.Context) (*listen.Listener, func(context.Context) (net.Conn, error)) {
	t.Helper()
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	name := `\\.\pipe\fernhill-press-` + rand.Text()
	l, err := listen.Listen(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	l.Listener = peer.Listener(l.Listener, peer.Principals(me.Uid), nil)
	return l, func(ctx context.Context) (net.Conn, error) {
		return peer.DialPipe(ctx, name)
	}
}
