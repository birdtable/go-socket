package peer

import (
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// peerOf is c's peer as the kernel recorded it when that end connected
// (SO_PEERCRED, unix(7)), the user and the process. The socket's domain is
// read first: for a TCP socket the kernel answers with the uid 4294967295,
// (uid_t)-1, as if it were a user, and a door trusting it would take every TCP
// peer. A unix socket the kernel recorded no peer for answers with that uid
// too, and a peer this process's user namespace does not map answers with the
// overflow uid; neither is ever a user (errNoPeer, errUnmapped).
func peerOf(c net.Conn) (Peer, error) {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return Peer{}, errNoSocket
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var (
		domain          int
		cred            *unix.Ucred
		domErr, credErr error
	)
	if err := raw.Control(func(fd uintptr) {
		domain, domErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_DOMAIN)
		if domErr != nil || domain != unix.AF_UNIX {
			return
		}
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	switch {
	case domErr != nil:
		return Peer{}, domErr
	case domain != unix.AF_UNIX:
		return Peer{}, errNotUnix
	case credErr != nil:
		return Peer{}, credErr
	case cred.Uid == noPeerUID:
		return Peer{}, errNoPeer
	case unmapped(cred.Uid, readUIDMap, readOverflowUID):
		return Peer{}, errUnmapped
	}
	return Peer{User: Unix(cred.Uid), PID: int(cred.Pid), PIDKnown: cred.Pid > 0}, nil
}

// noPeerUID is the uid SO_PEERCRED gives for a unix socket the kernel recorded
// no peer for: a datagram socket, or a stream one that never connected. It is
// (uid_t)-1, which no credential the kernel records holds.
const noPeerUID = math.MaxUint32

// errNoPeer is PeerOf's error for a unix socket the kernel recorded no peer
// for.
var errNoPeer = fmt.Errorf("the kernel recorded no peer for this unix socket: %w", ErrCannotSay)

// errUnmapped is PeerOf's error for a peer whose user this process's user
// namespace does not map: the kernel gives such a user as the overflow uid,
// which is the namespace's own nobody too, so two users would read as one.
var errUnmapped = fmt.Errorf("the peer's user is not one this process's user namespace maps: %w", ErrCannotSay)

// The files the kernel says this process's user namespace in (user_namespaces(7)).
const (
	procUIDMap      = "/proc/self/uid_map"
	procOverflowUID = "/proc/sys/kernel/overflowuid"
)

// defaultOverflowUID is the overflow uid where its file cannot be read: the
// kernel's own default.
const defaultOverflowUID = 65534

// readUIDMap and readOverflowUID read the two files.
func readUIDMap() ([]byte, error)      { return os.ReadFile(procUIDMap) }
func readOverflowUID() ([]byte, error) { return os.ReadFile(procOverflowUID) }

// unmapped is whether uid, as SO_PEERCRED gives it, may be a user this
// process's namespace does not map: uid is the overflow uid readOverflow
// gives, and the map readMap gives is not the whole identity map (one line,
// "0 0 4294967295"), which the first namespace has. The namespace's own user
// with that uid is refused with it: the kernel answers the same for both. A
// file that cannot be read, or does not parse, never clears a uid: the map is
// then not the identity, and the overflow uid the default. The map is read
// only for the overflow uid.
func unmapped(uid uint32, readMap, readOverflow func() ([]byte, error)) bool {
	return uid == overflowUID(readOverflow()) && !identityMap(readMap())
}

// overflowUID is the uid in b, the overflow uid file as read, or
// defaultOverflowUID.
func overflowUID(b []byte, err error) uint32 {
	if err != nil {
		return defaultOverflowUID
	}
	uid, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 32)
	if err != nil {
		return defaultOverflowUID
	}
	return uint32(uid)
}

// identityMap is whether b, the uid map file as read, is the whole identity
// map, read as numbers: one line of three, 0, 0 and 4294967295.
func identityMap(b []byte, err error) bool {
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 1 {
		return false
	}
	f := strings.Fields(lines[0])
	if len(f) != 3 {
		return false
	}
	var n [3]uint64
	for i, s := range f {
		if n[i], err = strconv.ParseUint(s, 10, 32); err != nil {
			return false
		}
	}
	return n == [3]uint64{0, 0, math.MaxUint32}
}
