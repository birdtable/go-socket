//go:build linux || darwin || freebsd

package peer

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

// The tests here run where the kernel says a unix peer's user: Linux, macOS
// and FreeBSD.

// tcpConn is a connection from a TCP address: nothing but its address is
// read.
type tcpConn struct {
	net.Conn
	remote net.Addr
}

func (c tcpConn) RemoteAddr() net.Addr { return c.remote }

// fromAddr is a connection from addr, written host:port, that is no socket.
func fromAddr(addr string) net.Conn {
	return tcpConn{remote: net.TCPAddrFromAddrPort(netip.MustParseAddrPort(addr))}
}

// gate is an address a gate connects from.
const gate = "192.0.2.10:50000"

// PeerOf is the user of the process at the other end of a unix socket, and
// ErrCannotSay for a TCP connection on loopback, refused as no unix socket's
// before the kernel is asked: on Linux it would answer with the uid 4294967295,
// (uid_t)-1, which a door trusting that uid would take, and on macOS with an
// answer that holds no group, which a reader taking it unchecked would read as
// a user. A connection that is no socket is ErrCannotSay too.
//
// What it costs when this fails: every door's socket answers any process
// on the machine, or a TCP peer passes as a user.
func TestPeerOfIsTheUserAtTheOtherEnd(t *testing.T) {
	t.Parallel()
	p, err := PeerOf(unixPeer(t))
	if uid, ok := p.UID(); err != nil || p.User != Unix(me()) || !ok || uid != me() {
		t.Errorf("a unix peer: %+v %v, want the user %s", p, err, Unix(me()))
	}
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var d net.Dialer
	c, err := d.DialContext(context.Background(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if p, err := PeerOf(s); !errors.Is(err, errNotUnix) || !errors.Is(err, ErrCannotSay) || p != (Peer{}) {
		t.Errorf("a TCP connection: %+v and %v, want no peer and ErrCannotSay, as no unix socket's", p, err)
	}
	if p, err := PeerOf(fromAddr(gate)); !errors.Is(err, ErrCannotSay) || p != (Peer{}) {
		t.Errorf("a connection that is no socket: %+v and %v, want no peer and ErrCannotSay", p, err)
	}
}

// Principals trusts the principals it names alone, by their exact spelling,
// and never the empty principal of a peer that could not be read.
//
// What it costs when this fails: a door trusts a user it was not told to, or
// refuses the one it was, and the person who runs the program is locked out
// or any account on the machine is let in.
func TestPrincipalsTrustsOnlyThoseItNames(t *testing.T) {
	t.Parallel()
	if !Principals(Unix(me())).trusts(Unix(me())) {
		t.Errorf("its own principal %s was not trusted", Unix(me()))
	}
	others := []struct {
		p    Peers
		user string
	}{
		{Principals(), Unix(me())},
		{Peers{}, Unix(me())},
		{Principals(Unix(me() + 1)), Unix(me())},
		{Principals(Unix(1000)), "uid:01000"},
		{Principals(""), ""},
		{Principals(Unix(me())), ""},
	}
	refused := 0
	for _, o := range others {
		if o.p.trusts(o.user) {
			t.Errorf("%v trusted %q", o.p, o.user)
		}
		refused++
	}
	if refused != len(others) {
		t.Fatalf("held %d of %d other principals", refused, len(others))
	}
}

// Unix writes a uid as its principal, and Peer.UID reads one back only from
// that exact spelling: another system's principal, a sign, a leading zero, or
// a number past a uid's 32 bits is no Unix user.
//
// What it costs when this fails: a program reads "uid:01000" or "uid:4294968296"
// as user 1000 and acts for them, or takes a Windows SID for a uid.
func TestUIDReadsOnlyAUnixPrincipalAsUnixWritesIt(t *testing.T) {
	t.Parallel()
	for _, uid := range []uint32{0, 1000, 1<<32 - 1} {
		if got, ok := (Peer{User: Unix(uid)}).UID(); !ok || got != uid {
			t.Errorf("%s: %d %v, want %d", Unix(uid), got, ok, uid)
		}
	}
	notUnix := []string{"", "uid:", "uid:01000", "uid:+1000", "uid:-1", "uid:4294967296", "uid:4294968296", "uid: 1000", "UID:1000", "1000", "S-1-5-21-1-2-3-1001"}
	refused := 0
	for _, user := range notUnix {
		if got, ok := (Peer{User: user}).UID(); ok || got != 0 {
			t.Errorf("%q read as the uid %d", user, got)
		}
		refused++
	}
	if refused != len(notUnix) {
		t.Fatalf("held %d of %d principals that are no uid", refused, len(notUnix))
	}
}
