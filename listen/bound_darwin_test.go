package listen

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// Rule 4 on darwin: a live socket whose queue is full refuses a stream connection (ECONNREFUSED)
// as one a run before left does, so with no option it is refused with ErrMaybeBusy, as every
// socket that refuses is. With ReplaceEvenIfBusy, Listen asks again by a datagram connection,
// which darwin answers with EPROTOTYPE where a stream socket is bound, listening or not
// (a run on darwin-arm64 showed it), and refuses it with ErrHeld. Either way the socket is left at its name, the
// lock let go, and once its queue drains a dial reaches it.
//
// What it costs when this fails: a program started with ReplaceEvenIfBusy beside a busy older one
// that takes no lock removes its socket, and the older one, still running, is cut off from every
// visitor.
func TestOnDarwinASocketWhoseQueueIsFullIsLeft(t *testing.T) {
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
			if err := syscall.Listen(fd, 0); err != nil {
				t.Fatal(err)
			}
			const most = 1024
			filled := false
			for range most {
				c, err := dial(t, unixNet, path)
				if refused(err) {
					filled = true
					break
				}
				if err != nil {
					t.Fatalf("a connection to fill the queue: %v", err)
				}
				t.Cleanup(func() { _ = c.Close() })
			}
			if !filled {
				t.Fatalf("the queue took %d connections and never refused one", most)
			}
			was, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			l, err := Listen(soon(t), path, row.opts...)
			if err == nil {
				_ = l.Close()
				t.Fatalf("Listen took %s, where a live socket's queue is full, and want it refused", path)
			}
			if !errors.Is(err, row.want) {
				t.Fatalf("a live socket whose queue is full: %v, and want %v", err, row.want)
			}
			sameFile(t, "a full queue's refusal", path, was)
			leaves(t, "a full queue's refusal", dir, "app.sock", "app.sock.lock")
			lockFree(t, "a full queue's refusal", path)
			nfd, _, err := syscall.Accept(fd)
			if err != nil {
				t.Fatalf("the socket left does not give up a queued connection: %v", err)
			}
			_ = syscall.Close(nfd)
			c, err := dial(t, unixNet, path)
			if err != nil {
				t.Fatalf("the socket left does not answer once its queue has room: %v", err)
			}
			_ = c.Close()
		})
	}
}
