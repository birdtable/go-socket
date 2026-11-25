package listen

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

// Listen is a listener of the program's own, where nothing hands it a socket:
// in a container, or a test. On Unix a path (starting / or ., or absolute) is
// a Unix socket; on Windows a pipe's name (\\.\pipe\ and then a name) is a
// named pipe; anything else is a TCP address. An empty address is refused
// with ErrNoAddress, and nothing is made. A pipe's name off Windows, and a
// path or anything else starting \ on Windows, is refused with ErrPipeName,
// and nothing is made.
//
// ctx bounds what Listen waits on: the one connection it makes to a socket
// at its path, to ask whether a program answers there, and the TCP listen.
// The span is the caller's, so a ctx with no deadline is refused, naming the
// deadline, and nothing is made.
//
// # A Unix socket
//
// A path is made absolute first, against the folder the program is in when
// it calls Listen: the lock file, the socket and the Close that removes it
// all use that path, whatever folder the program moves to after.
//
// A Unix socket's listener holds the lock file <path>.lock for its life, and
// Listen decides in this order:
//
//  1. The lock is held by another program: refused, with ErrHeld.
//  2. Something at the path is not a socket: refused, and left as it is.
//  3. The socket at the path answers a connection, or says its queue is
//     full (EAGAIN, as Linux does): refused, with ErrHeld.
//  4. It refuses the connection, as one nothing listens on does: refused
//     with ErrMaybeBusy and left, on every system and kernel, unless the
//     caller gave ReplaceEvenIfBusy. With it, the socket is replaced (on
//     Linux and darwin, once a datagram connection says no socket is bound
//     to it still; one bound is refused with ErrHeld and left).
//  5. The connection fails any other way: refused, and the socket is left.
//
// Listen removes only the socket rule 4 replaces, while the path is still it,
// and then puts its socket at the path without removing anything: a name
// another program took since Listen looked is refused and left, and Listen
// fails. The refusal is the hard link's, an error that wraps ErrMaybeBusy,
// as every other answer of a name there does, and fs.ErrExist (at v0.2.0 it
// was the bind's, EADDRINUSE).
//
// The lock covers programs that listen through Listen, while <path>.lock
// keeps its name: a program that removes it under a live listener leaves
// that listener rules 3 to 5. Rule 4 cannot tell a socket a run before left
// from a live one of a program that takes no lock. macOS and the BSDs refuse
// a connection to a full queue as they refuse one to a socket nothing
// listens on. And on every system rule 4's removal is a look and then an
// unlink by path, with nothing holding the name between them, so a program
// that takes no lock and unlinks and binds at the path in that gap loses its
// live socket. No kernel closes the gap, so rule 4 removes nothing the caller
// did not say it may: ReplaceEvenIfBusy is the caller's word that no program
// that takes no lock listens at the path. On Linux and darwin, with it, a
// socket bound and not yet listening is still left: it refuses a stream
// connection as a run before's socket does, but a datagram connection finds
// it (EPROTOTYPE, where a run before's says ECONNREFUSED). On darwin so is a
// live socket whose queue is full, which refuses a stream connection the
// same way. The BSDs ask nothing more: what they answer is unmeasured.
// noneBound says what that ask waits on, and from which kernel.
//
// The lock file is opened only as a plain file of its own name: a symbolic
// link there, a folder or anything else that is not a plain file is refused
// with ErrLockNotAFile, and nothing is made where a link points. The lock is
// let go on every refusal, and by Close after the socket is removed; the lock
// file itself is never removed, since a program that removes one can take it
// while another still holds it.
//
// Close removes the socket bound and nothing else, with no option given, so
// a clean restart finds nothing at the path. It removes the path only while
// both hold: <path>.lock is still the lock file it holds, and the path is
// still the socket Listen bound, by os.SameFile against what os.Lstat said of
// it once bound, looked at while the socket is still open (an open socket
// holds its file, so no other file is given its device and inode number).
// Otherwise the path is left, a socket another program put there since or a
// file, and Close says so with ErrSocketLeft.
//
// That look is proved the socket's before Listen answers: once the socket
// listens, Listen looks at the path, dials it with a fresh nonce, accepts on
// the socket bound, and looks again. Only a dial accepted there with the
// nonce, between two looks at the same socket file, proves the look the
// socket's: an unlink and a bind cannot put back a socket file once unlinked
// (a hard link renamed back by a program of the same account can, which the
// proof does not take on: such a program can as well remove the socket). A
// path another program's socket took since the bind fails the proof, and so
// does a path another program unlinked and nothing is at when the proof
// looks: Listen closes its socket, leaves the path, and fails with
// ErrMaybeBusy. When a proof's dial fails, ctx having ended among the ways,
// Listen removes the path while it is still its own socket's file, which the
// socket's private name in its private folder still names, so a Listen that
// fails for its own reason leaves no socket at the path, and fails with the
// dial's own error, which wraps ctx's, and not with ErrMaybeBusy: nothing
// refusing is left there. Another program's socket there is left, and that
// failure wraps ErrMaybeBusy. A connection a client
// made before that dial is not lost: it is handed, unread, to the first
// Accept. A failure before the socket is at its path removes nothing there,
// and Listen's error is the failure's own, with no ErrSocketLeft: nothing of
// Listen's was at the path.
//
// One window is left, in the two places Listen removes without
// ReplaceEvenIfBusy: Close's look and unlink, and a failed proof's, are by
// path, so a program that takes no lock and unlinks and binds at the path in
// the instant between them loses its socket. A test that raced Close so on
// darwin-arm64 met it in 5, 0 and 0 of 20,000 rounds, and in none in four
// more runs with longer delays.
//
// The socket is bound with no access at all in a private folder beside the
// path, <folder>/.<6 random hex>, made 0700, given its mode and group there
// by its private name, and listens; only then is it put at the path, by a
// hard link (link(2)) that refuses a name already there and leaves it, and
// its private name and folder are removed, on every way out. Nobody connects
// to it before it has its mode and group, and they are set on its own file
// alone: set by path at the path, they would land on another program's
// socket bound there in the gap, which a test that raced a bind so met in 45,
// 38 and 41 of 20,000 rounds on darwin. So the socket's
// folder must take hard links, as Unix's own filesystems do (FAT, exFAT and
// some network shares do not), and Listen must be let make a folder there,
// link and remove names in it, and remove it: under an AppArmor profile or
// an SELinux policy that grants only the socket and its lock file, Listen
// fails, and nothing is left at the path. A private folder that cannot be
// removed once the socket is at its path does not fail Listen: it is 0700,
// nothing in it is reached, and Close tries again once the socket is
// closed, and says so if that fails too. On an NFS client the private name,
// unlinked while the socket holds it, stays in the folder as a .nfsXXXX name
// until then. A process killed while it starts may leave its private folder,
// which no later start removes. The socket's own address is still its
// private name, removed once the proof is done: a client's getpeername(2) and
// RemoteAddr, ss -xl and /proc/net/unix say that name. The listener's Addr
// and its connections' LocalAddr are the path: a connection Accept answers
// is not itself a *net.UnixConn, its own address being the private name, but
// has every method of one, SyscallConn among them. Its folder is the
// caller's to check first: one another account may write can have the
// socket swapped under it.
//
// A path, made absolute, is at most what a Unix socket's address holds on the
// system, 107 bytes on Linux and 103 on macOS and the BSDs: a longer one is
// refused, naming the path and the limit, before anything is made. The
// socket's private name must fit too, its folder and 10 bytes more: a path
// whose private name does not is refused, naming the path, the private
// folder and the limit, before anything is made.
//
// # A named pipe, on Windows
//
// The caller names the pipe: \\.\pipe\ and then a name with no \ or / in it
// that does not end in a dot or a space, which Win32 drops. Listen gives it
// no default. The pipe's owner and access list are set as the pipe is made,
// so there is no moment it is open to anyone else: the account the program
// runs as owns it, an elevated administrator's too, and may read and write it
// and make its next server end; SYSTEM may do anything; nothing is
// inherited, and the kernel refuses everyone else at the dial. Elevation is
// no line here, as it is none for a Unix socket's uid: a process of the same
// account, elevated or not, may dial the pipe, make its next server end and,
// as its owner, rewrite its list.
// A pipe of that name already there, made by anybody, is refused with
// ErrHeld, never joined: Windows answers the first server end's FILE_CREATE
// there with ERROR_ACCESS_DENIED, or with ERROR_PIPE_BUSY where the pipe's
// holder bounded its instances and made them all, and ErrHeld wraps either.
// A token that may make no pipe, in an app container or under a restricted
// token, meets ERROR_ACCESS_DENIED at a free name too: where Windows answers
// that no pipe is at the name, that Listen is refused with an error that
// wraps fs.ErrPermission instead; any other answer, and a name holding a
// character Windows takes as a wildcard (* ? < > "), stays ErrHeld. That
// answer reads the name at that moment and is no proof of a missing right:
// a pipe another program makes and closes between the create and the look
// gives it too. An ErrHeld error for ERROR_ACCESS_DENIED matches
// fs.ErrPermission as well, since it wraps that Errno, so test ErrHeld first.
// Clients on other machines are refused. Peer's PeerOf says which account
// each connection is, before a byte is read, for a client that dials with
// peer.DialPipe; peer's Listener keeps the ones a program trusts.
//
// While a server end of the pipe is open, the listener's or a connection
// Accept gave, which holds the name past the listener's Close until it
// closes too, a Listen at its name is ErrHeld; the process's exit closes
// them all. Don't count on a client that keeps its end to hold the name: on
// Windows 11 (build 26200) the name was seen free once the last server end
// closed, and a restarted program's Listen took it, while that client's end
// reached nothing of the new pipe. A Windows that kept the name would refuse
// that Listen with ErrHeld until the client closed. There is no lock file, no rule 2 to 5 and nothing for Close to
// remove. SocketMode, SocketGroup and ReplaceEvenIfBusy, which mean something
// for a Unix socket alone, are refused with an error that wraps
// errors.ErrUnsupported, and nothing is made.
func Listen(ctx context.Context, addr string, opts ...ListenOption) (*Listener, error) {
	if addr == "" {
		return nil, ErrNoAddress
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, fmt.Errorf("listen.Listen(%q): ctx has no deadline: the span Listen waits on is the caller's, so give ctx one", addr)
	}
	if !isLocal(addr) {
		l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		return &Listener{Listener: l, name: addr}, nil
	}
	o := listen{mode: 0o600}
	for _, opt := range opts {
		opt(&o)
	}
	l, err := listenLocal(ctx, addr, o)
	if err != nil {
		return nil, err
	}
	return &Listener{Listener: l, name: addr, local: true}, nil
}

// isLocal says whether addr names a listener on this machine, a socket's path
// or a pipe's name, rather than a TCP address: it starts /, . or \, or is
// absolute on this system (C:\ on Windows). listenLocal says which of them
// this system takes.
func isLocal(addr string) bool {
	return strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, ".") || strings.HasPrefix(addr, `\`) || filepath.IsAbs(addr)
}
