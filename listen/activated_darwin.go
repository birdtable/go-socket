package listen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"github.com/bored-engineer/go-launchd"
)

// Activated is the listeners launchd passed this process under each of names,
// a key of the job's Sockets, in the order named, each with its key for its
// Name; a key that names two sockets, as a port on IPv4 and IPv6, gives both.
// They are asked of launchd's launch_activate_socket, which hands each key's
// sockets once: a second Activated for a key is refused. A process launchd
// did not start, or whose job has no socket under one of names, is
// ErrNotActivated: launchd answers the two alike. Each descriptor launchd
// hands is marked close-on-exec as it is taken, so no program this process
// starts inherits it. One passed a descriptor
// that is not a stream socket listening is refused. No names, or one that is
// empty or named twice, is refused with fs.ErrInvalid before launchd is
// asked, as launchd passes nothing unnamed. On every refusal each listener
// made and each descriptor passed is closed.
func Activated(names ...string) ([]*Listener, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("launchd passes each socket under its key in the job's Sockets, and no key was named: %w", fs.ErrInvalid)
	}
	if err := distinct(names); err != nil {
		return nil, err
	}
	out := make([]*Listener, 0, len(names))
	for _, n := range names {
		files, err := launchdFiles(n)
		switch {
		case errors.Is(err, syscall.ESRCH) || err == nil && len(files) == 0:
			err = fmt.Errorf("launchd passed no socket named %q to this process: %w", n, ErrNotActivated)
		case errors.Is(err, syscall.EALREADY):
			err = fmt.Errorf("the socket named %q was taken already, and launchd hands it once: %w", n, err)
		case err != nil:
			err = fmt.Errorf("launchd's socket named %q: %w", n, err)
		}
		if err != nil {
			return nil, joined(err, errors.Join(closeAll(out), closeFiles(files)))
		}
		ls, err := listenersOf(files, func(*os.File) string { return n }, "the job's Sockets says SockType stream for each")
		if err != nil {
			return nil, joined(err, closeAll(out))
		}
		out = append(out, ls...)
	}
	return out, nil
}

// launchdFiles is launchd.Files for the key name, each descriptor marked
// close-on-exec at once: go-launchd wraps them with os.NewFile and marks none,
// so a program this process starts before they are closed would inherit a
// listening socket. syscall.ForkLock is held for reading from the ask to the
// last mark, as the standard library holds it where a socket cannot be made
// close-on-exec as it is made, so no program starts in between.
func launchdFiles(name string) ([]*os.File, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	files, err := launchd.Files(name)
	closeOnExec(files)
	return files, err
}

// closeOnExec marks each of files close-on-exec. launchd on macOS 27 hands
// its descriptors marked already; go-launchd does not say so, and nothing
// documents it, so each is marked here whatever launchd did.
func closeOnExec(files []*os.File) {
	for _, f := range files {
		syscall.CloseOnExec(int(f.Fd()))
	}
}
