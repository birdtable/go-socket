//go:build unix

package listen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// socketDir is a folder for a test's sockets, removed when the test ends. It
// is under /tmp and not t.TempDir or os.TempDir: on macOS those are a long
// path under /var/folders, which breaks the 104-byte limit on a socket's
// address.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "listen")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// maxSocketPath is the most bytes a test's socket path may have: macOS holds a
// Unix socket's address to 104 bytes with its NUL, Linux to 108, and the tests
// keep 4 under the least.
const maxSocketPath = 100

// socketPath is name in dir, for a test's socket. It fails the test, naming the
// path, when the path is over maxSocketPath, so a test whose folder grew is
// told so and not taken for a refusal of Listen's.
func socketPath(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if why := socketTooLong(p); why != "" {
		t.Fatal(why)
	}
	return p
}

// socketTooLong says why p is too long for a test's socket, or is "" when it fits.
func socketTooLong(p string) string {
	if len(p) > maxSocketPath {
		return fmt.Sprintf("the socket path %q is %d bytes, over the %d a test's socket may have", p, len(p), maxSocketPath)
	}
	return ""
}

// A test's socket path is refused, by name, when it is over maxSocketPath, and
// taken at exactly the limit.
//
// What it costs when this fails: a test whose socket folder grew past the address limit
// fails inside Listen, and reads as a fault of listen's and not of the test's own folder.
func TestATestsSocketPathOverTheLimitIsRefusedByName(t *testing.T) {
	t.Parallel()
	sized := func(n int) string { return "/tmp/" + strings.Repeat("d", n-len("/tmp/")) }
	cases := []struct {
		path    string
		refused bool
	}{
		{sized(maxSocketPath - 1), false},
		{sized(maxSocketPath), false},
		{sized(maxSocketPath + 1), true},
	}
	ran := 0
	for _, tc := range cases {
		ran++
		why := socketTooLong(tc.path)
		if tc.refused && !strings.Contains(why, strconv.Quote(tc.path)) {
			t.Errorf("%d bytes: refused as %q, which does not name it", len(tc.path), why)
		}
		if !tc.refused && why != "" {
			t.Errorf("%d bytes: refused as %q", len(tc.path), why)
		}
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
}

// local is an address of the test's own for a listener on this machine: a
// socket's path, in a folder of its own.
func local(t *testing.T) string {
	t.Helper()
	return socketPath(t, socketDir(t), "app.sock")
}

// dialLocal connects to the local listener at addr.
func dialLocal(ctx context.Context, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, unixNet, addr)
}

// childAt is where the listening child listens, started in dir: app.sock
// there, a path of the child's own and not one handed to it, so nothing from
// outside reaches Listen.
func childAt(string) string { return "./app.sock" }

// A pipe's name off Windows is refused with ErrPipeName, naming it, and
// nothing is made: not a socket at a relative path made of it, nor its lock
// file.
//
// What it costs when this fails: a program configured for Windows' pipe runs
// on Unix and makes a socket named \\.\pipe\app in its folder, which nothing
// dials, or a TCP listener of the name.
func TestAPipesNameOffWindowsIsRefusedWithErrPipeName(t *testing.T) {
	dir := socketDir(t)
	t.Chdir(dir)
	for _, addr := range []string{`\\.\pipe\app`, `\app.sock`} {
		l, err := Listen(soon(t), addr)
		if err == nil {
			_ = l.Close()
		}
		if !errors.Is(err, ErrPipeName) || !strings.Contains(fmt.Sprint(err), strconv.Quote(addr)) {
			t.Errorf("Listen(%q) off Windows: %v, and want ErrPipeName, naming it", addr, err)
		}
	}
	leaves(t, "a pipe's name off Windows", dir)
}

// listenOn listens on an address of the test's own, and gives up with the test.
func listenOn(t *testing.T, network, addr string) (net.Listener, error) {
	t.Helper()
	return (&net.ListenConfig{}).Listen(t.Context(), network, addr)
}

// kill ends the child at once, as a crash would, leaving its socket where it was.
func (c *child) kill(t *testing.T) {
	t.Helper()
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, c.said)
	_ = c.cmd.Wait()
}

// localFolder is a folder for a test's Local, removed when the test ends: short, as a socket's
// path must be.
func localFolder(t *testing.T) string {
	t.Helper()
	return socketDir(t)
}

// localWant is the address Local gives for name in folder, an absolute folder, on Unix: the
// path of name in it.
func localWant(_ *testing.T, folder, name string) string { return filepath.Join(folder, name) }

// A folder whose path for name is longer than a Unix socket's address holds, or whose socket's
// private name is, is refused by Local before anything is made, with the error Listen gives that
// path, naming it: Local moves no socket to a shorter folder.
//
// What it costs when this fails: a program is told an address Listen then refuses, after it
// told its client; or Local answers a shorter folder of its own, and the caller's socket is
// somewhere it did not choose.
func TestAFolderTooLongForASocketIsRefusedByLocalAsByListen(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	cases := []struct {
		what, folder, name, says string
	}{
		{"the path", filepath.Join(dir, strings.Repeat("f", sunPath-len(dir))), "agent", "a Unix socket's path holds at most"},
		{"the private name", filepath.Join(dir, strings.Repeat("g", sunPath-len(dir)-3)), "a", "private folder"},
	}
	ran := 0
	for _, tc := range cases {
		ran++
		if err := os.Mkdir(tc.folder, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(tc.folder, tc.name)
		_, err := Local(tc.folder, tc.name)
		l, lerr := Listen(soon(t), path)
		if lerr == nil {
			_ = l.Close()
			t.Fatalf("Listen(%q), %s too long: listened, and want it refused", path, tc.what)
		}
		if err == nil || !strings.Contains(err.Error(), lerr.Error()) || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("Local(%q, %q), %s too long: %v, and want Listen's error, %q", tc.folder, tc.name, tc.what, err, lerr)
		}
		leaves(t, "Local refused "+tc.what, tc.folder)
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
}
