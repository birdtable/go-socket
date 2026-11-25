//go:build !linux && !darwin && !freebsd && !windows

package peer

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// pipes is a listener whose Accept hands out the server ends of pipes, one
// per client end it was given, then net.ErrClosed.
type pipes struct {
	net.Listener
	servers []net.Conn
}

func (l *pipes) Accept() (net.Conn, error) {
	if len(l.servers) == 0 {
		return nil, net.ErrClosed
	}
	c := l.servers[0]
	l.servers = l.servers[1:]
	return c, nil
}

// On a system peer does not cover, the BSDs but FreeBSD, PeerOf
// is ErrCannotSay, which is errors.ErrUnsupported, Principals trusts nobody,
// and Listener refuses every connection as a peer it cannot read, closing it,
// so a program built here serves nobody rather than everybody.
//
// What it costs when this fails: a program built for one of those BSDs takes a
// peer it cannot name as one it trusts, or cannot tell an unsupported system
// from a failed read and retries a read that never comes.
func TestEveryPeerCallIsUnsupportedWherePeerDoesNotReadOne(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	if p, err := PeerOf(server); !errors.Is(err, ErrCannotSay) || !errors.Is(err, errors.ErrUnsupported) || p != (Peer{}) {
		t.Errorf("PeerOf = %+v, %v, want no peer and ErrCannotSay", p, err)
	}

	var clients []net.Conn
	l := &pipes{}
	const opened = 2
	for range opened {
		s, c := net.Pipe()
		t.Cleanup(func() { _ = s.Close(); _ = c.Close() })
		l.servers = append(l.servers, s)
		clients = append(clients, c)
	}
	type refusal struct {
		principal string
		known     bool
	}
	var refused []refusal
	got, err := Listener(l, Principals(Unix(0), Unix(1000)), func(principal string, known bool) {
		refused = append(refused, refusal{principal, known})
	}).Accept()
	if got != nil || !errors.Is(err, net.ErrClosed) {
		t.Errorf("Accept = %v, %v, want no connection and the listener's own error", got, err)
	}
	if len(refused) != len(clients) {
		t.Fatalf("refused %d of %d connections: %v", len(refused), len(clients), refused)
	}
	checked := 0
	for i, r := range refused {
		checked++
		if r != (refusal{"", false}) {
			t.Errorf("refusal %d said the principal %q, known %v, want none known", i, r.principal, r.known)
		}
		if err := clients[i].SetWriteDeadline(time.Now().Add(time.Second)); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal(err)
		}
		if _, err := clients[i].Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("the refused connection %d is still open to its client", i)
		}
	}
	if checked != opened {
		t.Fatalf("checked %d refused connections, want the %d opened", checked, opened)
	}
}
