//go:build unix

package listen

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
)

// Close removes the socket only while the path is still the socket Listen bound: a socket
// another program put there since, or a file, is left as it is, Close says so with
// ErrSocketLeft, and the lock is let go.
//
// What it costs when this fails: a program that shuts down removes the socket a newer one
// listens at, and every visitor is cut off from it, or removes somebody's file by its name.
func TestCloseLeavesWhatAnotherProgramPutAtThePath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		plant func(t *testing.T, path string) func(t *testing.T)
	}{
		{"another program's socket", func(t *testing.T, path string) func(t *testing.T) {
			t.Helper()
			other, err := listenOn(t, unixNet, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = other.Close() })
			return func(t *testing.T) {
				t.Helper()
				c, err := dial(t, unixNet, path)
				if err != nil {
					t.Errorf("the other program's socket no longer answers: %v", err)
					return
				}
				_ = c.Close()
			}
		}},
		{"a file", func(t *testing.T, path string) func(t *testing.T) {
			t.Helper()
			if err := os.WriteFile(path, []byte("notes"), 0o600); err != nil {
				t.Fatal(err)
			}
			return func(t *testing.T) {
				t.Helper()
				if b, err := os.ReadFile(path); err != nil || string(b) != "notes" {
					t.Errorf("the file now reads %q (%v)", b, err)
				}
			}
		}},
	}
	ran := 0
	for _, tc := range cases {
		ran++
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		l, err := Listen(soon(t), path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("the socket Listen bound could not be taken away for %s: %v", tc.name, err)
		}
		check := tc.plant(t, path)
		was, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Close(); !errors.Is(err, ErrSocketLeft) {
			t.Errorf("Close with %s at the path: %v, and want ErrSocketLeft", tc.name, err)
		}
		sameFile(t, "Close with "+tc.name+" at the path", path, was)
		check(t)
		leaves(t, "Close with "+tc.name+" at the path", dir, "app.sock", "app.sock.lock")
		lockFree(t, "Close with "+tc.name+" at the path", path)
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
}

// A relative path is made absolute when Listen is called: the socket's address is the absolute
// path, and a Close after the program moves to another folder removes the socket it bound, and
// not a file of the same name in the folder it is in now. Not parallel: it moves the process.
//
// What it costs when this fails: a program that changes folder after it listens removes a file
// of somebody's by its name on Close, and leaves its own socket behind.
func TestARelativePathIsTheFolderListenWasCalledIn(t *testing.T) {
	dir, there := socketDir(t), socketDir(t)
	t.Chdir(dir)
	l, err := Listen(soon(t), "./app.sock")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := os.Lstat(filepath.Join(dir, "app.sock"))
	if err != nil {
		t.Fatalf("the socket is not in the folder Listen was called in: %v", err)
	}
	if !filepath.IsAbs(l.Addr().String()) || l.Name() != "./app.sock" {
		t.Errorf("the listener is named %q at %q, and want ./app.sock at an absolute path", l.Name(), l.Addr())
	}
	if at, err := os.Lstat(l.Addr().String()); err != nil || !os.SameFile(at, bound) {
		t.Errorf("the listener's address %q is not the socket it bound (%v)", l.Addr(), err)
	}
	t.Chdir(there)
	if err := os.WriteFile("app.sock", []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(there, "app.sock")); err != nil || string(b) != "notes" {
		t.Errorf("Close in another folder took its file of the same name: it reads %q (%v)", b, err)
	}
	leaves(t, "Close in another folder", dir, "app.sock.lock")
	leaves(t, "Close in another folder", there, "app.sock")
}

// Close leaves the socket while <path>.lock is no longer the lock file it holds: one that
// removed it under the live listener may have made another and locked it, and listens at the
// path or is about to. Close says so with ErrSocketLeft, and lets its own lock go all the same.
//
// What it costs when this fails: a program that shuts down removes the path while another holds
// the lock there, and that one's socket, bound or about to be, is cut off from every visitor.
func TestCloseLeavesThePathWhileAnotherHoldsTheLock(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	l, err := Listen(soon(t), path)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	plantLock(t, path)
	if err := l.Close(); !errors.Is(err, ErrSocketLeft) {
		t.Errorf("Close while another holds the lock: %v, and want ErrSocketLeft", err)
	}
	sameFile(t, "Close while another holds the lock", path, bound)
	leaves(t, "Close while another holds the lock", dir, "app.sock", "app.sock.lock")
}

// A failure while the socket has only its private name, before it listens and is put at its
// path, removes nothing at the path: the path is left whatever it is, the socket's private name
// and folder are removed, and Listen's error is the failure's own, with no ErrSocketLeft, as
// nothing of Listen's was at the path. Here nothing is at the path, or another program that
// takes no lock bound and listens there meanwhile. The lock is let go.
//
// What it costs when this fails: a program whose start fails removes the live socket of one that
// took the path meanwhile, and every visitor of that one is cut off; or it leaves a private
// folder behind at every failed start.
func TestAFailureBeforeTheSocketIsAtItsPathRemovesNothingThere(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		other bool
	}{
		{"nothing", false},
		{"another program's socket", true},
	}
	ran := 0
	for _, tc := range cases {
		ran++
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		failed := errors.New("the probe fails the start before the socket is at its path")
		var was os.FileInfo
		l, err := Listen(soon(t), path, withProbe(func(string) error {
			if tc.other {
				other, err := listenOn(t, unixNet, path)
				if err != nil {
					return err
				}
				t.Cleanup(func() { _ = other.Close() })
				if was, err = os.Lstat(path); err != nil {
					return err
				}
			}
			return failed
		}))
		if !errors.Is(err, failed) || errors.Is(err, ErrSocketLeft) {
			if err == nil {
				_ = l.Close()
			}
			t.Fatalf("a start that fails before the socket is at its path, with %s there: %v, and want the failure and no ErrSocketLeft", tc.name, err)
		}
		want := []string{"app.sock.lock"}
		if tc.other {
			want = append(want, "app.sock")
			sameFile(t, "a failure before the path, "+tc.name+" there", path, was)
			c, err := dial(t, unixNet, path)
			if err != nil {
				t.Errorf("the other program's socket no longer answers: %v", err)
			} else {
				_ = c.Close()
			}
		}
		leaves(t, "a failure before the path, "+tc.name+" there", dir, want...)
		lockFree(t, "a failure before the path, "+tc.name+" there", path)
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
}

// The socket's private folder is removed on every way out of Listen: once it listens, and when
// it fails before the socket is at its path (an option fails), when the path is taken (the link
// is refused), and when the proof fails once it is there (another program unlinked it). Each
// leaves the folder as Listen found it but for the lock and, once it listens, the socket. A
// folder that cannot be removed once the socket is at its path, something else being in it (as
// an NFS client keeps the private name there as .nfsXXXX while the socket is open), does not
// fail Listen: Close tries again once the socket is closed, and removes it when what was in it
// has gone; when it has not, Close leaves it with what is there and says so, and still removes
// the socket and lets the lock go. A folder that still holds something when a failed proof
// removes it is left with what is there, and Listen's error names it, since no Close follows.
//
// What it costs when this fails: every start leaves a folder behind beside the socket, or every
// start on an NFS folder fails, or a folder Close could not remove goes unsaid.
func TestThePrivateFolderIsRemovedOnEveryWayOut(t *testing.T) {
	t.Parallel()
	failed := errors.New("the probe fails the start")
	cases := []struct {
		name string
		opts func(t *testing.T, path string) []ListenOption
		// want is what Listen's error wraps, nil when it listens.
		want error
		// left is what the folder holds after Listen, the lock aside.
		left []string
	}{
		{"it listens", func(*testing.T, string) []ListenOption { return nil }, nil, []string{"app.sock"}},
		{"an option fails", func(*testing.T, string) []ListenOption {
			return []ListenOption{withProbe(func(string) error { return failed })}
		}, failed, nil},
		{"the path is taken", func(t *testing.T, path string) []ListenOption {
			t.Helper()
			return []ListenOption{withProbe(func(string) error {
				other, err := listenOn(t, unixNet, path)
				if err == nil {
					t.Cleanup(func() { _ = other.Close() })
				}
				return err
			})}
		}, ErrMaybeBusy, []string{"app.sock"}},
		{"the proof fails", func(t *testing.T, path string) []ListenOption {
			t.Helper()
			return []ListenOption{withEarlyProbe(func(p string) {
				if err := os.Remove(p); err != nil {
					t.Error(err)
				}
			})}
		}, ErrMaybeBusy, nil},
	}
	ran := 0
	for _, tc := range cases {
		ran++
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		l, err := Listen(soon(t), path, tc.opts(t, path)...)
		switch {
		case tc.want == nil && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != nil && !errors.Is(err, tc.want):
			t.Errorf("%s: %v, and want %v", tc.name, err, tc.want)
		}
		leaves(t, tc.name, dir, append([]string{"app.sock.lock"}, tc.left...)...)
		if l != nil {
			if err := l.Close(); err != nil {
				t.Errorf("%s: Close: %v", tc.name, err)
			}
		}
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
	for _, gone := range []bool{true, false} {
		what := "a private folder Listen could not remove, and Close can"
		if !gone {
			what = "a private folder neither Listen nor Close can remove"
		}
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		var private string
		// The seam: a file in the private folder fails its removal, as .nfsXXXX does on NFS.
		l, err := Listen(soon(t), path, withProbe(func(p string) error {
			private = filepath.Dir(p)
			return os.WriteFile(filepath.Join(private, "notes"), nil, 0o600)
		}))
		if err != nil {
			t.Fatalf("%s: Listen: %v, and want it to listen", what, err)
		}
		leaves(t, what+", once Listen answered", dir, "app.sock", "app.sock.lock", filepath.Base(private))
		leaves(t, what+", inside, once Listen answered", private, "notes")
		reaches(t, what, path, l)
		if gone {
			// As NFS removes .nfsXXXX once the socket closes.
			if err := os.Remove(filepath.Join(private, "notes")); err != nil {
				t.Fatal(err)
			}
		}
		err = l.Close()
		switch {
		case gone && err != nil:
			t.Errorf("%s: Close: %v", what, err)
		case !gone && (err == nil || !strings.Contains(err.Error(), private) || errors.Is(err, ErrSocketLeft)):
			t.Errorf("%s: Close: %v, and want it to name the folder %s left, and no ErrSocketLeft", what, err, private)
		}
		if gone {
			leaves(t, what, dir, "app.sock.lock")
		} else {
			leaves(t, what, dir, "app.sock.lock", filepath.Base(private))
			leaves(t, what+", inside", private, "notes")
		}
		lockFree(t, what, path)
	}
	// A private folder that still holds something when a failed proof removes it: the proof fails
	// (another program unlinked the path), the folder cannot be removed, and Listen's error says
	// both, ErrMaybeBusy and the folder by name. The folder is left, with what it holds; its socket's
	// private name is gone, and nothing is at the path.
	what := "a private folder that holds something when a failed proof removes it"
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	var private string
	l, err := Listen(soon(t), path, withProbe(func(p string) error {
		private = filepath.Dir(p)
		return os.WriteFile(filepath.Join(private, "notes"), nil, 0o600)
	}), withEarlyProbe(func(p string) {
		if err := os.Remove(p); err != nil {
			t.Error(err)
		}
	}))
	if l != nil {
		_ = l.Close()
		t.Fatalf("%s: Listen listens, and want it to fail", what)
	}
	if !errors.Is(err, ErrMaybeBusy) || !strings.Contains(err.Error(), private) || errors.Is(err, ErrSocketLeft) {
		t.Errorf("%s: %v, and want ErrMaybeBusy, naming the folder %s left, and no ErrSocketLeft", what, err, private)
	}
	leaves(t, what, dir, "app.sock.lock", filepath.Base(private))
	leaves(t, what+", inside", private, "notes")
	lockFree(t, what, path)
}

// reaches fails t unless a dial to path is accepted by l.
func reaches(t *testing.T, what, path string, l net.Listener) {
	t.Helper()
	accepted := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			err = c.Close()
		}
		accepted <- err
	}()
	c, err := dial(t, unixNet, path)
	if err != nil {
		t.Fatalf("%s: the socket does not answer at its path: %v", what, err)
	}
	_ = c.Close()
	if err := <-accepted; err != nil {
		t.Fatalf("%s: the socket did not accept the dial: %v", what, err)
	}
}

// A name another program put at the path between Listen's look and its link, a file, a dangling
// symbolic link or a socket that listens, refuses the link: Listen fails with an error that wraps
// ErrMaybeBusy, as every other answer of a name there does, and fs.ErrExist, the link's own. The
// name is left as it was (the symbolic link's target is not made), the private folder is gone,
// and the lock is let go.
//
// What it costs when this fails: a caller that answers ErrMaybeBusy, by asking whether to give
// ReplaceEvenIfBusy, reads a name taken in that gap as a failure of its own.
func TestANameTakenBeforeTheLinkIsMaybeBusyAndLeft(t *testing.T) {
	t.Parallel()
	kinds := []string{"a file", "a dangling symbolic link", "a socket that listens"}
	ran := 0
	for _, kind := range kinds {
		ran++
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		target := filepath.Join(dir, "target")
		var other net.Listener
		var put os.FileInfo
		probe := withBindProbe(func(bool) {
			var err error
			switch kind {
			case "a file":
				err = os.WriteFile(path, []byte("theirs"), 0o600)
			case "a dangling symbolic link":
				err = os.Symlink(target, path)
			default:
				other, err = listenOn(t, unixNet, path)
			}
			if err == nil {
				put, err = os.Lstat(path)
			}
			if err != nil {
				t.Fatalf("%s, put at the path: %v", kind, err)
			}
		})
		l, err := Listen(soon(t), path, probe)
		if other != nil {
			t.Cleanup(func() { _ = other.Close() })
		}
		if put == nil {
			t.Fatalf("%s: Listen linked without running its bind probe: %v", kind, err)
		}
		if !errors.Is(err, ErrMaybeBusy) || !errors.Is(err, fs.ErrExist) {
			if err == nil {
				_ = l.Close()
			}
			t.Errorf("%s at the path before the link: %v, and want ErrMaybeBusy and fs.ErrExist", kind, err)
		}
		sameFile(t, kind, path, put)
		leaves(t, kind, dir, "app.sock", "app.sock.lock")
		lockFree(t, kind, path)
		switch kind {
		case "a file":
			if b, err := os.ReadFile(path); err != nil || string(b) != "theirs" {
				t.Errorf("%s: it holds %q (%v), and want what the other program wrote", kind, b, err)
			}
		case "a dangling symbolic link":
			if to, err := os.Readlink(path); err != nil || to != target {
				t.Errorf("%s: it points at %q (%v), and want %s", kind, to, err, target)
			}
		default:
			reachesOther(t, path, other)
		}
	}
	if ran != len(kinds) {
		t.Fatalf("ran %d of %d kinds", ran, len(kinds))
	}
}

// removeOwn leaves the path, and says so with ErrSocketLeft, when the lock it is given is not
// held: with no lock held, the lock's own look would be by path, and pass for any lock file
// there, held by anyone.
//
// What it costs when this fails: a Close that runs once its lock is let go removes the socket of
// one that took the lock since and bound the path.
func TestRemoveOwnLeavesThePathWithNoLockHeld(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	other, err := listenOn(t, unixNet, path)
	if err != nil {
		t.Fatal(err)
	}
	other.(*net.UnixListener).SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = other.Close() })
	bound, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeOwn(path, bound, flock.New(path+".lock")); !errors.Is(err, ErrSocketLeft) {
		t.Errorf("removeOwn with no lock held: %v, and want ErrSocketLeft", err)
	}
	sameFile(t, "removeOwn with no lock held", path, bound)
	leaves(t, "removeOwn with no lock held", dir, "app.sock", "app.sock.lock")
}

// Close removes the socket Listen bound, with no option given, and then lets the lock go: the
// folder holds the lock file alone. It removes it before it closes the socket, while the lock
// file is still the one held and the path still the socket bound.
//
// What it costs when this fails: every clean restart finds the socket a run before left and is
// refused with ErrMaybeBusy, so every program gives ReplaceEvenIfBusy always, and that option then
// removes other programs' live sockets.
func TestCloseRemovesItsOwnSocket(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	l, err := Listen(soon(t), path)
	if err != nil {
		t.Fatal(err)
	}
	leaves(t, "a listener", dir, "app.sock", "app.sock.lock")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	leaves(t, "Close", dir, "app.sock.lock")
	lockFree(t, "Close", path)
}
