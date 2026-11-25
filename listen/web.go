// Package listen is how a program answers over HTTP and still costs nothing
// between visits: started by its socket when a request arrives (systemd's
// socket activation, through coreos/go-systemd), gone a while after the last
// request (Idle), or listening on a socket of its own under a lock file, or
// on Windows a named pipe of its own (Listen).
//
// A visit to a page is twenty-odd requests: one process for the visit, not
// one per request, and none between visits.
//
// It is built on coreos/go-systemd's activation, docker/go-connections'
// socket options, which give a Unix socket its mode and group before it
// listens, gofrs/flock's lock, Microsoft/go-winio's named pipe on Windows,
// and net/http. It adds what they lack: the rules that tell a socket a run
// before left from one a live program holds, a bind that removes nothing, a
// Close that removes only the socket it bound, the idle exit, and on Windows
// the pipe's access list, its name checked, and a pipe already there refused
// as held.
// The program configures the path, pipe name or address, the socket's mode (0600 unless
// it gives another) and group, each listener's handler, what a connection
// adds to its requests, and every span: ctx's deadline, Grace,
// ReadHeaderTimeout, KeepAlive and NewIdle's after and linger, with no
// default.
package listen

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
)

// Listener is one place the program answers, its name, and whether it is a
// socket on this machine: on a server, only the gate in front reaches one,
// which a caller may trust further than a TCP port.
type Listener struct {
	net.Listener
	name  string
	local bool
	// ConnContext, when set, adds the program's own to each connection's
	// context on this listener alone, as http.Server's ConnContext does. Serve
	// runs it after Options.ConnContext, on the context that returned: it must
	// build on the ctx it is given, since a fresh one drops what that added,
	// and a check of the peer reading it then refuses every request. It must
	// not return nil: net/http panics in the accept loop, which ends the
	// program, so it fails closed.
	ConnContext func(ctx context.Context, c net.Conn) context.Context
}

// Name is the listener's name: the path, pipe name or address Listen was given, or the
// name systemd passed with the socket (the unit's FileDescriptorName=, or
// LISTEN_FD_<n> when it gave none).
func (l *Listener) Name() string { return l.name }

// Local says whether the listener is a Unix socket or, on Windows, a named
// pipe, which only a program on this machine reaches, and not a TCP port.
func (l *Listener) Local() bool { return l.local }

// ErrNotActivated is a process that systemd or launchd did not start by the
// socket named, and every process off Linux and macOS, where nothing passes
// one: it wraps errors.ErrUnsupported.
var ErrNotActivated = fmt.Errorf("not started by its socket: systemd or launchd starts this when a request arrives: %w", errors.ErrUnsupported)

// ErrHeld is a path another program listens at: it holds the path's lock, or
// its socket answers. On Windows it is a pipe's name a pipe is already made
// at, by any program, which Listen never joins.
var ErrHeld = errors.New("another program holds it")

// ErrPipeName is a name Listen does not take on this system: on Windows,
// anything starting \, /, . or a drive that is not \\.\pipe\ and then a name
// with no \ or / in it that does not end in a dot or a space, a path among
// them; elsewhere, a name starting \, as a pipe's does. Nothing is made.
var ErrPipeName = errors.New(`a pipe's name on Windows is \\.\pipe\ and then a name with no \ or / in it that does not end in a dot or a space, and a socket's path elsewhere`)

// ErrNoAddress is Listen given an empty address, which names neither a path
// nor a TCP address: nothing is made.
var ErrNoAddress = errors.New("no address: give Listen a socket's path or a TCP address")

// ErrLockNotAFile is a lock file's name that is not a plain file: a symbolic
// link, a folder, or anything else. Listen
// refuses it, leaves it as it is, and makes nothing where a link points.
var ErrLockNotAFile = errors.New("the lock file's name is not a plain file")

// ErrMaybeBusy is a socket at the path that refuses a connection, left there
// because the caller did not give ReplaceEvenIfBusy, on every system and
// kernel. Off Linux a busy program that takes no lock refuses a connection as
// a socket nothing listens on does, and on every system a program that takes
// no lock may unlink and bind at the path between rule 4's look and its
// removal, so the removal is the caller's word that no such program listens
// there. Listen fails with it too when the path, once bound, is not its own
// socket by its proof: another program that takes no lock unlinked and bound
// there since, and its socket is left, or unlinked it and nothing is there at
// one of the proof's looks. The text names ReplaceEvenIfBusy for
// the first case; the second wraps it with what it is. A proof whose dial
// fails for Listen's own reason, its ctx having ended among the ways, while
// the path is still its own socket, does not wrap it: Listen removes that
// socket, and the error is the dial's own.
var ErrMaybeBusy = errors.New("a socket that refuses a connection is at the path, and it was not removed: give ReplaceEvenIfBusy to replace it")

// ErrSocketLeft is a socket's path that Close left as it is instead of
// removing it: the path no longer names the
// socket bound, or <path>.lock is no longer the lock file held, so what is
// there may be another program's. The listener is closed and its lock let go
// all the same.
var ErrSocketLeft = errors.New("the socket's path was left as it is")

// closeAll closes each listener and says the errors closing them gave, joined, or nil.
func closeAll(ls []*Listener) error {
	var err error
	for _, l := range ls {
		err = errors.Join(err, l.Close())
	}
	return err
}

// joined is err with cerr, the error of a close that came after it, said as well; err itself when the
// close was clean, so what a caller compares or matches is as it was.
func joined(err, cerr error) error {
	if cerr == nil {
		return err
	}
	return errors.Join(err, cerr)
}

// ListenOption is one way a socket of the program's own differs from the
// default: 0600, and its writer's group.
// Each system's file declares listen, the options as given, with the fields
// its own listener reads.
type ListenOption func(*listen)

// SocketMode is the socket's mode: 0660 lets SocketGroup's members connect.
// The mode must let its owner write (0200): Listen dials the socket to prove
// its path, and a connect needs write, so a mode without it is refused with
// an error that wraps fs.ErrInvalid before anything is made. It is a Unix
// socket's alone: on Windows, whose pipe has its own access list instead
// (Listen says which), Listen refuses it with an error that wraps
// errors.ErrUnsupported.
func SocketMode(mode os.FileMode) ListenOption {
	return func(l *listen) { l.mode, l.modeSet = mode&os.ModePerm, true }
}

// SocketGroup gives the socket to a group, by name: the gate in front runs
// in it. It is a Unix socket's alone: on Windows, whose pipe has no group,
// Listen refuses it with an error that wraps errors.ErrUnsupported.
func SocketGroup(group string) ListenOption { return func(l *listen) { l.group = group } }

// ReplaceEvenIfBusy lets Listen replace a socket at the path that refuses a
// connection, on every system and kernel, knowing it may be a busy or
// starting program's. Without it, Listen refuses such a socket with
// ErrMaybeBusy and leaves it. Give it only where no program that takes no
// lock listens at the path: one that listens through Listen holds the lock,
// and is refused with ErrHeld before its socket is asked. With it, a program
// that takes no lock and unlinks and binds at the path between rule 4's look
// and its removal loses its live socket: on every system, as the removal is a
// look and then an unlink by path, and also where the filesystem gives the
// removed socket's inode number to the new one (ext4, overlayfs). On Linux
// and darwin, with it, a socket bound and not yet listening when Listen asks
// is still left, with ErrHeld, and on darwin so is a live socket whose queue
// is full; the BSDs, unmeasured, ask nothing more. It is a Unix socket's alone: on Windows, where a pipe
// leaves no file to replace, Listen refuses it with an error that wraps
// errors.ErrUnsupported.
func ReplaceEvenIfBusy() ListenOption { return func(l *listen) { l.evenIfBusy = true } }
