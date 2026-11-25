package listen

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// Rule 3 on Linux: a socket whose queue is full answers a connection with EAGAIN, as a program
// that listens and is busy does. It is refused with ErrHeld and left, with the lock let go.
// macOS and the BSDs refuse such a connection as one to a socket nothing listens on,
// which cannot be told apart: Listen's doc and the README say so.
//
// What it costs when this fails: a program started beside a busy older one that takes no lock
// removes its socket, and the older one, still running, is cut off from every visitor.
func TestASocketWhoseQueueIsFullIsRefusedWithErrHeld(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}
	const most = 64
	filled := false
	for range most {
		c, err := dial(t, unixNet, path)
		if errors.Is(err, syscall.EAGAIN) {
			filled = true
			break
		}
		if err != nil {
			t.Fatalf("a connection to fill the queue: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
	}
	if !filled {
		t.Fatalf("the queue took %d connections and never said it was full", most)
	}
	was, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if l, err := Listen(soon(t), path); !errors.Is(err, ErrHeld) {
		if err == nil {
			_ = l.Close()
		}
		t.Fatalf("a socket whose queue is full: %v, and want ErrHeld", err)
	}
	sameFile(t, "a full queue's refusal", path, was)
	leaves(t, "a full queue's refusal", dir, "app.sock", "app.sock.lock")
	lockFree(t, "a full queue's refusal", path)
}
