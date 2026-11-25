//go:build darwin || freebsd

package peer

import (
	"errors"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// peerOf is c's peer as the kernel recorded it when that end connected
// (LOCAL_PEERCRED, unix(4)): the user, read by xucredUser. The kernel does not
// give the process here, so PIDKnown is false.
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
		local            bool
		cred             *unix.Xucred
		nameErr, credErr error
	)
	if err := raw.Control(func(fd uintptr) {
		var sa unix.Sockaddr
		sa, nameErr = unix.Getsockname(int(fd))
		_, local = sa.(*unix.SockaddrUnix)
		if nameErr != nil || !local {
			return
		}
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	switch {
	case nameErr != nil:
		return Peer{}, nameErr
	case !local:
		return Peer{}, errNotUnix
	case credErr != nil:
		return Peer{}, fmt.Errorf("the kernel would not say the peer's user: %w", credErr)
	}
	uid, err := xucredUser(cred)
	if err != nil {
		return Peer{}, err
	}
	return Peer{User: Unix(uid)}, nil
}

// xucredVersion is the version of struct xucred the user is read from
// (XUCRED_VERSION in sys/ucred.h, on macOS and FreeBSD).
const xucredVersion = 0

// errXucred is xucredUser's refusal of an answer it does not read a user from.
var errXucred = errors.New("the kernel's answer for the peer's user is not one read here")

// xucredUser is the user in the kernel's answer to LOCAL_PEERCRED, which
// x/sys reads into a zeroed struct xucred and does not say how much of it the
// kernel wrote. An answer of another version, or one with no group, is
// errXucred and never a user: the user comes before the count of groups, and
// every process has at least one, so a count of one or more says the user was
// written, and a short answer, whose missing bytes stay zero and would read as
// uid 0, root, is refused.
func xucredUser(x *unix.Xucred) (uint32, error) {
	if x.Version != xucredVersion || x.Ngroups < 1 {
		return 0, fmt.Errorf("%w: version %d (only %d is read), %d groups (at least 1 is written)", errXucred, x.Version, xucredVersion, x.Ngroups)
	}
	return x.Uid, nil
}
