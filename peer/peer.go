package peer

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
)

// Peer is who is at the other end of a local connection, as the kernel
// recorded it when that end connected. User is a principal: "uid:1000" on
// Unix (Unix), and on Windows the account's SID as Windows writes it,
// "S-1-5-21-...-1001". PID is the process that connected, where PIDKnown says the
// kernel gave one, on Linux alone; it may have ended since and its number gone
// to another, so it names who connected, never who is there now, and decides
// nothing.
type Peer struct {
	User     string
	PID      int
	PIDKnown bool
}

// unixUser is the prefix of a Unix user's principal.
const unixUser = "uid:"

// Unix is the principal of the Unix user uid, as Peer.User holds it and
// Principals takes it.
func Unix(uid uint32) string { return unixUser + strconv.FormatUint(uint64(uid), 10) }

// UID is the Unix user p is, and false for a principal that is not one as
// Unix writes it: another system's, or "uid:" with anything but a uid's
// decimal digits after it, a sign or a leading zero among them.
func (p Peer) UID() (uint32, bool) {
	digits, ok := strings.CutPrefix(p.User, unixUser)
	if !ok {
		return 0, false
	}
	uid, err := strconv.ParseUint(digits, 10, 32)
	if err != nil || Unix(uint32(uid)) != p.User {
		return 0, false
	}
	return uint32(uid), true
}

// ErrCannotSay is a connection whose peer this system cannot name: one that is
// not a local socket's or, on Windows, a named pipe's, wherever it is, and
// every connection on a system peer does not read. ListenPipe and DialPipe are
// ErrCannotSay off Windows. It wraps errors.ErrUnsupported.
var ErrCannotSay = fmt.Errorf("the peer of this connection cannot be read here: %w", errors.ErrUnsupported)

// PeerOf is the peer at the other end of c, a unix socket, as the kernel
// recorded it when that end connected: SO_PEERCRED on Linux, LOCAL_PEERCRED on
// macOS and FreeBSD. Any other connection, a TCP one on loopback among them,
// is ErrCannotSay and never a peer: being local is not an identity. On Windows
// c is a named pipe's, from ListenPipe or DialPipe: on the listener's side the
// client's account, read from its token by impersonating it on a thread of its
// own, before a byte is read; on the dialling side the account that owns the
// pipe. A client that dialled anonymously, as go-winio's own dials do, is
// ErrCannotSay by name. So is every connection on the other BSDs. So is a unix
// socket the kernel recorded no peer for: a datagram one, or a stream one never connected.
// On Linux a program in a user namespace, a rootless container say, is told the
// user of a peer its namespace does not map as the overflow uid, 65534 unless
// /proc/sys/kernel/overflowuid says another, which is the namespace's own
// nobody too; where the namespace's map is not the whole identity, PeerOf
// refuses that uid as ErrCannotSay, the namespace's nobody with it. On an error
// the Peer is the zero Peer.
func PeerOf(c net.Conn) (Peer, error) { return peerOf(c) }

// Peers is the principals a program trusts, made by Principals. The zero Peers
// trusts nobody.
type Peers struct{ users []string }

// Principals trusts the peers whose principal is one of ps, written as Unix
// writes it or, for a Windows account, its SID as Windows writes it, and
// nobody else. No principal trusts nobody.
func Principals(ps ...string) Peers { return Peers{users: slices.Clone(ps)} }

// trusts is whether user is one of p's principals. The empty principal is
// nobody's.
func (p Peers) trusts(user string) bool {
	return user != "" && slices.Contains(p.users, user)
}

// Listener is l with every connection whose peer p does not trust closed as
// it is accepted, before a byte of it is read, so a stranger's request never
// reaches the program's handler or its parser. A connection whose peer PeerOf
// cannot read is refused too. refused, when not nil, is told of each: the
// peer's principal, and whether PeerOf read one (false, with "", for a peer it
// could not). Accept goes on to the next connection after a refusal, on the
// caller's goroutine, and returns only a trusted connection, l's error, or the
// error of closing a refused one: a connection this listener alone holds fails
// to close only if the program's own files are wrong, which no peer can bring
// about.
func Listener(l net.Listener, p Peers, refused func(principal string, known bool)) net.Listener {
	return gated{Listener: l, p: p, refused: refused}
}

type gated struct {
	net.Listener
	p       Peers
	refused func(principal string, known bool)
}

// Accept is the next connection of the listener whose peer is trusted.
func (g gated) Accept() (net.Conn, error) {
	for {
		c, err := g.Listener.Accept()
		if err != nil {
			return nil, err
		}
		who, err := PeerOf(c)
		if err == nil && g.p.trusts(who.User) {
			return c, nil
		}
		if err != nil {
			who = Peer{}
		}
		closeErr := c.Close()
		if g.refused != nil {
			g.refused(who.User, err == nil)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("closing a refused peer's connection: %w", closeErr)
		}
	}
}
