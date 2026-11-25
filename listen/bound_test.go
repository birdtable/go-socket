//go:build linux || darwin

package listen

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// Rule 4 on Linux and darwin: a socket that is bound and does not yet listen refuses a stream connection
// (ECONNREFUSED) as one a run before left does, and a program that takes no lock holds its
// socket so from its bind to its listen (go-connections does, while it gives the socket its mode
// and group). With no option it is refused with ErrMaybeBusy, as every socket that refuses is.
// With ReplaceEvenIfBusy, Listen asks again by a datagram connection, which a socket bound there
// answers, and refuses it with ErrHeld. Either way the socket is left bound at its name, the lock
// let go, and once its program listens a dial reaches it. The race between Listen and a
// program that takes no lock met this on linux-amd64; darwin answers the datagram connection
// the same way.
//
// What it costs when this fails: a program that starts beside one that takes no lock removes
// its socket in the moment between its bind and its listen; both then listen, and the other,
// still running, is cut off from every visitor.
func TestASocketBoundAndNotListeningIsLeft(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name string
		opts []ListenOption
		want error
	}{
		{"no option", nil, ErrMaybeBusy},
		{"ReplaceEvenIfBusy", []ListenOption{ReplaceEvenIfBusy()}, ErrHeld},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			dir := socketDir(t)
			path := socketPath(t, dir, "app.sock")
			fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Close(fd) })
			syscall.CloseOnExec(fd) // macOS has no SOCK_CLOEXEC; no child the tests start takes it.
			if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
				t.Fatal(err)
			}
			if c, err := dial(t, unixNet, path); !refused(err) {
				if err == nil {
					_ = c.Close()
				}
				t.Fatalf("a dial to a socket bound and not listening says %v, and want it refused", err)
			}
			was, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			l, err := Listen(soon(t), path, row.opts...)
			if err == nil {
				_ = l.Close()
				t.Fatalf("Listen took %s, where a socket is bound and not yet listening, and want it refused", path)
			}
			if !errors.Is(err, row.want) {
				t.Fatalf("a socket bound and not listening: %v, and want %v", err, row.want)
			}
			sameFile(t, "a bound socket's refusal", path, was)
			leaves(t, "a bound socket's refusal", dir, "app.sock", "app.sock.lock")
			lockFree(t, "a bound socket's refusal", path)
			if err := syscall.Listen(fd, 1); err != nil {
				t.Fatal(err)
			}
			c, err := dial(t, unixNet, path)
			if err != nil {
				t.Fatalf("the socket left bound does not answer once it listens: %v", err)
			}
			_ = c.Close()
		})
	}
}

// Rule 4 on Linux and darwin fails closed where it cannot wait on a bind under way: noneBound renames the
// path onto itself first, and a rename that fails (the file gone since Listen looked, or a
// folder on a read-only mount) leaves the path as it is, with an error that is not ErrHeld, and
// makes nothing there.
//
// What it costs when this fails: rule 4 goes on to remove a file whose bind it did not wait for,
// which may be a starting program's socket.
func TestRule4LeavesWhatItCannotWaitOn(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	err := noneBound(path)
	if err == nil {
		t.Fatalf("noneBound on %s, where nothing is, says none is bound, and want it to fail closed", path)
	}
	if errors.Is(err, ErrHeld) {
		t.Fatalf("noneBound on a missing path says %v, and want an error that is not ErrHeld", err)
	}
	var le *os.LinkError
	if !errors.As(err, &le) || le.Op != "rename" || !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("noneBound on a missing path says %v, and want the rename's ENOENT, before any connection is tried", err)
	}
	leaves(t, "a rename that failed", dir)
}
