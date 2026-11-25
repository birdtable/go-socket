//go:build unix

package listen

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// withProbe has Listen run f on the socket last of what it is given, before
// it listens: what nobody but a test can ask of it.
func withProbe(f func(path string) error) ListenOption {
	return func(l *listen) { l.probe = f }
}

// Rule 5: a socket that cannot be asked whether a program listens there, one
// this account may not connect to, is refused, and left, with the lock let go.
//
// What it costs when this fails: a program removes the socket of another
// account's program, which it could not ask, and that one is cut off from
// every visitor while it still runs.
func TestASocketThatCannotBeAskedIsLeft(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root writes every file")
	}
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	other, err := listenOn(t, unixNet, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	was, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Listen(soon(t), path)
	if err == nil {
		_ = l.Close()
	}
	if err == nil || errors.Is(err, ErrHeld) || !errors.Is(err, syscall.EACCES) || !strings.Contains(err.Error(), "could not be asked") {
		t.Errorf("a socket this account may not connect to: %v, and want a refusal that says it could not be asked", err)
	}
	sameFile(t, "an unaskable socket's refusal", path, was)
	leaves(t, "an unaskable socket's refusal", dir, "app.sock", "app.sock.lock")
	lockFree(t, "an unaskable socket's refusal", path)
}

// A group that is not there is refused before anything is made: the folder
// stays empty, with no lock file and no socket.
//
// What it costs when this fails: a program given a group by a typo leaves a
// socket nobody listens on, which its next start, with the group fixed, is
// refused at with ErrMaybeBusy (a failure once bound removes nothing).
func TestAGroupThatIsNotThereIsRefusedBeforeAnythingIsMade(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	l, err := Listen(soon(t), path, SocketGroup("no-such-group-here"))
	if err == nil {
		_ = l.Close()
		t.Fatal("a group that is not there was taken")
	}
	leaves(t, "a missing group's refusal", dir)
}

// The socket has the mode and the group asked for from the moment it can be
// reached, under a umask of 0, which would leave a socket bound plainly open
// to every account. Listen's last step before it listens finds the socket at
// its private name already with them, and no connection, and nothing yet at
// its path; and a dialler racing Listen
// finds them on the socket when it first connects, which the test waits for.
// The default is 0600, and
// the group is one of this account's others when it has one, so a chown that
// did not run is seen. Where this process cannot give a file that group, as in
// a user namespace that maps only the account's own, or the group has no name
// for SocketGroup to take, that row is skipped and the test's output says why;
// the default's row runs all the same, so the test itself never skips
// (api/contract.txt names it, and a test it names is never skipped).
//
// What it costs when this fails: between the bind and a chmod, anybody on the
// machine connects to a socket the gate alone was meant to reach.
func TestASocketHasItsModeAndGroupFromTheMomentItCanBeReached(t *testing.T) {
	// serial: it starts a child process, and a fork while another test holds a lock file open
	// holds that lock until the exec, so a parallel test would find its lock taken.
	const name = "TestASocketHasItsModeAndGroupFromTheMomentItCanBeReached"
	if os.Getenv(umaskChildEnv) == "1" {
		underUmaskZero(t)
		return
	}
	// The umask is the shell's to set before the exec: the process's own is
	// one for all its threads, and the other tests' files would have it too.
	cmd := exec.CommandContext(t.Context(), "sh", "-c", `umask 0 && exec "$0" -test.v -test.count=1 -test.run="^$1\$"`, os.Args[0], name)
	cmd.Env = append(quickExit(os.Environ()), umaskChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+name) {
		t.Errorf("under a umask of 0 (%v):\n%s", err, out)
	}
	// A row the child skipped says so here, so a run that is green says what it
	// did not check.
	for line := range strings.Lines(string(out)) {
		if _, why, ok := strings.Cut(line, rowSkipped); ok {
			t.Logf("%s%s", rowSkipped, strings.TrimSpace(why))
		}
	}
}

// dialerReach is how long the mode test waits, once the socket listens, for
// its dialler's first reach: a dial to a listening socket on this machine
// takes well under a millisecond, so this is a hang's bound, not a pace.
const dialerReach = 10 * time.Second

// rowSkipped starts the line the child logs for a row it cannot run, which
// the parent prints in its own output.
const rowSkipped = "row skipped: "

// underUmaskZero is the mode test's own work, in the child the shell started
// with a umask of 0.
func underUmaskZero(t *testing.T) {
	t.Helper()
	dir := socketDir(t)
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(plain); err != nil || fi.Mode().Perm() != 0o666 {
		t.Fatalf("a file made 0666 here is %v (%v): the umask is not 0, so this proves nothing", fi.Mode().Perm(), err)
	}
	gid := os.Getgid()
	if gs, err := os.Getgroups(); err == nil {
		if i := slices.IndexFunc(gs, func(g int) bool { return g != gid }); i >= 0 {
			gid = gs[i]
		}
	}
	group, noGroup := groupName(gid)
	if noGroup == "" {
		noGroup = cannotChown(t, dir, gid)
	}
	cases := []struct {
		name string
		opts []ListenOption
		mode os.FileMode
		gid  int
		skip string
	}{
		{"the default", nil, 0o600, fileGroup(t, dir), ""},
		{"0640 and a group", []ListenOption{SocketMode(0o640), SocketGroup(group)}, 0o640, gid, noGroup},
	}
	ran := 0
	for i, tc := range cases {
		ran++
		if tc.skip != "" {
			t.Logf("%s%s: %s", rowSkipped, tc.name, tc.skip)
			continue
		}
		path := socketPath(t, dir, fmt.Sprintf("s%d.sock", i))
		has := func(at, when string) {
			t.Helper()
			fi, err := os.Lstat(at)
			if err != nil {
				t.Errorf("%s, %s: %v", tc.name, when, err)
				return
			}
			if g := gidOf(t, fi); fi.Mode().Perm() != tc.mode || g != tc.gid {
				t.Errorf("%s, %s: the socket is %v, group %d, and want %v, group %d", tc.name, when, fi.Mode().Perm(), g, tc.mode, tc.gid)
			}
		}
		probed := 0
		probe := func(p string) error {
			probed++
			// The probe is given the socket's private name: it is not at the path until it listens.
			has(p, "before it listens")
			if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s: before it listens the socket's path says %v, and want nothing there", tc.name, err)
			}
			if c, err := (&net.Dialer{}).DialContext(t.Context(), unixNet, p); err == nil {
				_ = c.Close()
				t.Errorf("%s: the socket took a connection before it listened", tc.name)
			}
			return nil
		}
		var stop atomic.Bool
		var wg sync.WaitGroup
		reached := make(chan struct{})
		wg.Go(func() {
			for !stop.Load() {
				c, err := (&net.Dialer{}).DialContext(t.Context(), unixNet, path)
				if err != nil {
					continue
				}
				has(path, "when a dialler reached it")
				_ = c.Close()
				close(reached)
				// The first reach is the moment the row checks, so the dialler
				// stops there. Nothing here accepts, and macOS refuses a dial
				// once 128 connections wait unaccepted on a Unix socket, closed
				// ones too: a dialler that kept on would fill the queue, and the
				// dial below would be refused.
				return
			}
		})
		l, err := Listen(soon(t), path, append(tc.opts, withProbe(probe))...)
		if err != nil {
			stop.Store(true)
			wg.Wait()
			t.Fatalf("%s: %v", tc.name, err)
		}
		has(path, "once it listens")
		c, err := dial(t, unixNet, path)
		if err != nil {
			t.Errorf("%s: the socket does not answer: %v", tc.name, err)
		} else {
			_ = c.Close()
		}
		// The dialler's check is the row's second proof, so the row waits for
		// it rather than stopping a dialler that may not have reached yet;
		// one that never reaches is red, not skipped.
		select {
		case <-reached:
		case <-time.After(dialerReach):
			t.Errorf("%s: the dialler did not reach the listening socket within %v, so its check never ran", tc.name, dialerReach)
		}
		stop.Store(true)
		wg.Wait()
		if probed != 1 {
			t.Errorf("%s: the probe ran %d times, and want once", tc.name, probed)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
}

// cannotChown is why this process cannot give a file in dir the group gid,
// or "" when it can. In a user namespace that maps only the account's own
// group, as unshare --user --map-current-user does, a chown to any other of
// its groups is EINVAL, and one to a group it is not in is EPERM: the row
// that sets such a group cannot run there, and the library's own chown error
// is not what it checks.
func cannotChown(t *testing.T, dir string, gid int) string {
	t.Helper()
	p := filepath.Join(dir, "chown")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := os.Lchown(p, -1, gid)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.EPERM):
		return fmt.Sprintf("this process cannot give a file group %d (%v), as in a user namespace that maps only the account's own group", gid, err)
	}
	t.Fatalf("a chown to group %d: %v", gid, err)
	return ""
}

// groupName is the name of the group gid, which SocketGroup takes, or, where it has none, why
// the row that gives it cannot run.
func groupName(gid int) (name, why string) {
	g, err := user.LookupGroupId(strconv.Itoa(gid))
	if err != nil {
		return "", fmt.Sprintf("this account's group %d has no name: %v", gid, err)
	}
	return g.Name, ""
}

// gidOf is the group fi belongs to.
func gidOf(t *testing.T, fi os.FileInfo) int {
	t.Helper()
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s has no owner the system says", fi.Name())
	}
	return int(st.Gid)
}

// fileGroup is the group a file made in dir is given when nobody chooses one:
// the folder's on macOS and the BSDs, the account's own on Linux, as a socket
// bound there is given it.
func fileGroup(t *testing.T, dir string) int {
	t.Helper()
	p := filepath.Join(dir, "group")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return gidOf(t, fi)
}

// The lock file's name changed between Listen's look and its lock is refused with
// ErrLockNotAFile, and nothing is made where a link points and no socket is made: a link put
// there before the open fails it (O_NOFOLLOW), a folder put there is opened and refused once
// locked, and a plain file swapped in once the lock is held is not the file locked.
//
// What it costs when this fails: anyone who may write the socket's folder times a swap of the
// lock file's name, and Listen makes a file where a link points, or listens holding a lock on a
// file nobody else will open, so a second start takes its socket.
func TestALockFileNameSwappedWhileListenTakesItIsRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		stage string
		swap  func(t *testing.T, dir, lock string)
		want  []string
	}{
		{"a link put there before the open", "open", func(t *testing.T, dir, lock string) {
			t.Helper()
			if err := os.Remove(lock); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(filepath.Join(dir, "elsewhere"), lock); err != nil {
				t.Error(err)
			}
		}, []string{"app.sock.lock"}},
		{"a folder put there before the open", "open", func(t *testing.T, _, lock string) {
			t.Helper()
			if err := os.Remove(lock); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(lock, 0o700); err != nil {
				t.Error(err)
			}
		}, []string{"app.sock.lock"}},
		{"a file swapped in once locked", "locked", func(t *testing.T, _, lock string) {
			t.Helper()
			if err := os.Rename(lock, lock+".old"); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(lock, nil, 0o600); err != nil {
				t.Error(err)
			}
		}, []string{"app.sock.lock", "app.sock.lock.old"}},
	}
	ran := 0
	for _, tc := range cases {
		ran++
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		swapped := 0
		l, err := Listen(soon(t), path, withLockProbe(func(stage string) {
			if stage == tc.stage {
				swapped++
				tc.swap(t, dir, path+".lock")
			}
		}))
		if !errors.Is(err, ErrLockNotAFile) {
			if err == nil {
				_ = l.Close()
			}
			t.Errorf("%s: %v, and want ErrLockNotAFile", tc.name, err)
		}
		if swapped != 1 {
			t.Errorf("%s: the swap ran %d times, and want once", tc.name, swapped)
		}
		missing(t, tc.name+"'s refusal", filepath.Join(dir, "elsewhere"))
		leaves(t, tc.name+"'s refusal", dir, tc.want...)
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
}

// A socket swapped for a file between its bind and the look Listen takes once it listens is
// refused, and the file left: Close would remove only what that look found.
//
// What it costs when this fails: Listen hands back a listener whose Close removes a file put
// at the path in its place, which is not the socket it bound.
func TestASocketSwappedForAFileOnceBoundIsRefusedAndLeft(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	l, err := Listen(soon(t), path, withProbe(func(p string) error {
		if err := os.Remove(p); err != nil {
			return err
		}
		return os.WriteFile(p, []byte("notes"), 0o600)
	}))
	if err == nil || !strings.Contains(err.Error(), "once bound") {
		if err == nil {
			_ = l.Close()
		}
		t.Fatalf("a socket swapped for a file once bound: %v, and want a refusal that says so", err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "notes" {
		t.Errorf("the file put in the socket's place reads %q (%v)", b, err)
	}
	leaves(t, "a swap once bound's refusal", dir, "app.sock", "app.sock.lock")
	lockFree(t, "a swap once bound's refusal", path)
}

// A path that fits a Unix socket's address, in a folder so long that the socket's private name
// there does not, is refused before anything is made, the lock file and the private folder
// included, naming the path and the limit; a path whose private name is exactly the limit is
// listened on and answers.
//
// What it costs when this fails: a path a few bytes short of the limit passes the check, and the
// bind at the private name fails with "invalid argument" on macOS, or is cut short on Linux, once
// the lock file and the private folder are made.
func TestAPathWhosePrivateNameIsTooLongIsRefusedBeforeAnythingIsMade(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	// folder makes a folder in dir whose path is n bytes long.
	folder := func(n int) string {
		t.Helper()
		p := filepath.Join(dir, strings.Repeat("d", n-len(dir)-len("/")))
		if len(p) != n {
			t.Fatalf("a folder of %d bytes was asked for, and %d made", n, len(p))
		}
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	long := folder(sunPath - 4)
	path := filepath.Join(long, "a")
	if bindLen(path) <= sunPath || len(path) > sunPath {
		t.Fatalf("%s is %d bytes and its private name %d, and want it to fit %d and its private name not", path, len(path), bindLen(path), sunPath)
	}
	_, err := Listen(soon(t), path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "private folder") || !strings.Contains(err.Error(), fmt.Sprintf("at most %d here", sunPath)) {
		t.Errorf("a path of %d bytes whose private name is %d: %v", len(path), bindLen(path), err)
	}
	leaves(t, "a long private name's refusal", long)
	fits := filepath.Join(folder(sunPath-len("/.000000/s")), "a")
	if bindLen(fits) != sunPath {
		t.Fatalf("%s's private name is %d bytes, and want %d", fits, bindLen(fits), sunPath)
	}
	l, err := Listen(soon(t), fits)
	if err != nil {
		t.Fatalf("a path whose private name is exactly %d bytes: %v", sunPath, err)
	}
	defer func() { _ = l.Close() }()
	c, err := dial(t, unixNet, fits)
	if err != nil {
		t.Fatalf("a socket whose private name was %d bytes does not answer: %v", sunPath, err)
	}
	_ = c.Close()
}
