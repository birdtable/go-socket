//go:build linux || darwin

package listen

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// noneBound says, before rule 4 removes the socket at path, that no socket
// is bound to it. Linux refuses a stream connection to a socket bound and not
// yet listening as it refuses one to a socket a run before left, so a program
// that takes no lock, caught between its bind and its listen, reads as a run
// before. A datagram connection tells them apart: the kernel finds the socket
// bound to the file before it checks its type, so it says ECONNREFUSED where
// none is bound, EPROTOTYPE where a stream socket is, and connects where a
// datagram one is. A socket bound there is ErrHeld; any other answer leaves
// the file as it is.
//
// A bind still under way is a socket file with no socket bound to it yet:
// the kernel makes the file, then binds the socket to it, and a connection in
// between finds none and says ECONNREFUSED. Linux's bind holds the file's
// folder locked over both steps (af_unix's unix_bind_bsd, from Linux 5.14),
// so noneBound first renames path onto itself, which takes that lock and
// changes nothing: once it returns, the bind that made the file has bound its
// socket, and the datagram connection finds it.
//
// What that relies on, in Linux 6.6's source: unix_bind_bsd takes the folder
// in kern_path_create (net/unix/af_unix.c:1232; fs/namei.c:3890), makes the
// file (af_unix.c:1244), binds the socket to it (unix_insert_bsd_socket,
// :1259), and lets the folder go only then (done_path_create, :1261). A
// rename takes the same folder (do_renameat2's lock_rename, namei.c:4952 and
// :3046) before it looks the names up, and vfs_rename returns at once for a
// file renamed onto itself (namei.c:4773). A connection finds a socket by
// its file's inode (unix_find_socket_byinode, af_unix.c:1093).
//
// darwin answers the same way, as a run on darwin 27.2.0 (arm64) showed
// it: ECONNREFUSED where no socket is bound, a run before's
// file or one whose socket closed since; EPROTOTYPE where a stream socket
// is bound, listening or not; and a connection where a datagram one is.
// That tells more there than on Linux: darwin refuses a stream connection
// to a live socket whose queue is full (ECONNREFUSED) as it refuses one to a
// run before's, and the datagram connection finds that socket too, so with
// ReplaceEvenIfBusy it is ErrHeld and left. The rename onto itself changes
// nothing on darwin either, but no lock of darwin's bind is known here that
// it waits on, so a bind under way may be missed, as before Linux 5.14.
//
// Before Linux 5.14 the bind lets the folder go once the file is made and
// binds the socket after (unix_mknod's done_path_create, then
// __unix_insert_socket: 5.13's af_unix.c:1007 and :1089), so the rename
// does not wait for it, and a bind under way may be missed. On every kernel,
// rule 4 reaches here only with ReplaceEvenIfBusy, the caller's word that no
// program that takes no lock binds at the path, and nothing holds the name
// from this answer to the removal: what this finds narrows the option, and
// guarantees nothing about what binds after.
func noneBound(path string) error {
	if err := syscall.Rename(path, path); err != nil {
		return fmt.Errorf("%s could not wait on a bind under way there, so it is left: %w", path, &os.LinkError{Op: "rename", Old: path, New: path, Err: err})
	}
	// Linux makes the socket close-on-exec in the one call (sockCloexec), as
	// main did. darwin has no SOCK_CLOEXEC, so there it is marked after, under
	// syscall.ForkLock, which keeps Go's own fork and exec out of the gap but
	// not a fork made by C code; go-socket has no cgo.
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM|sockCloexec, 0)
	if err == nil && sockCloexec == 0 {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return fmt.Errorf("%s could not be asked whether a socket is bound there, so it is left: %w", path, os.NewSyscallError("socket", err))
	}
	cerr := syscall.Connect(fd, &syscall.SockaddrUnix{Name: path})
	if err := syscall.Close(fd); err != nil {
		return fmt.Errorf("%s could not be asked whether a socket is bound there, so it is left: %w", path, os.NewSyscallError("close", err))
	}
	switch {
	case errors.Is(cerr, syscall.ECONNREFUSED):
		return nil
	case cerr == nil, errors.Is(cerr, syscall.EPROTOTYPE):
		return fmt.Errorf("%s refuses a connection, but a socket is bound there that does not yet listen: %w", path, ErrHeld)
	}
	return fmt.Errorf("%s could not be asked whether a socket is bound there, so it is left: %w", path, os.NewSyscallError("connect", cerr))
}
