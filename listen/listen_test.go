//go:build unix

package listen

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// left is the names in dir, sorted.
func left(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(es))
	for _, e := range es {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// leaves fails t unless dir holds want and nothing else, after the refusal said in what.
func leaves(t *testing.T, what, dir string, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := left(t, dir); !slices.Equal(got, want) {
		t.Errorf("after %s the folder holds %q, and want %q", what, got, want)
	}
}

// lockFree fails t unless the lock beside path can be taken: Listen let it go.
func lockFree(t *testing.T, what, path string) {
	t.Helper()
	f := flock.New(path + ".lock")
	ok, err := f.TryLock()
	if err != nil || !ok {
		t.Errorf("after %s the lock is still held (%v): Listen did not let it go", what, err)
	}
	if err := f.Unlock(); err != nil {
		t.Fatal(err)
	}
}

// plantLock holds the lock beside path, as another program listening there does, until the test ends.
func plantLock(t *testing.T, path string) *flock.Flock {
	t.Helper()
	f := flock.New(path + ".lock")
	if ok, err := f.TryLock(); err != nil || !ok {
		t.Fatalf("the planted lock: %v", err)
	}
	t.Cleanup(func() { _ = f.Unlock() })
	return f
}

// stale leaves a socket at path that nothing listens on, as a program that was killed does.
func stale(t *testing.T, path string) {
	t.Helper()
	l, err := listenOn(t, unixNet, path)
	if err != nil {
		t.Fatal(err)
	}
	ul, ok := l.(*net.UnixListener)
	if !ok {
		t.Fatalf("%s is not a Unix socket", path)
	}
	ul.SetUnlinkOnClose(false)
	if err := ul.Close(); err != nil {
		t.Fatal(err)
	}
}

// sameFile fails t unless path is still the file was.
func sameFile(t *testing.T, what, path string, was os.FileInfo) {
	t.Helper()
	now, err := os.Lstat(path)
	if err != nil || !os.SameFile(now, was) {
		t.Errorf("after %s, %s was removed or replaced (%v)", what, path, err)
	}
}

// Rule 1: a path whose lock another program holds is refused with ErrHeld, before anything at
// the path is asked: a socket there that nothing answers on is left too.
//
// What it costs when this fails: a second start of a program takes the socket of the first,
// which is still starting or between two accepts.
func TestAPathWhoseLockAnotherProgramHoldsIsRefusedAndLeft(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	lock := plantLock(t, path)
	stale(t, path)
	was, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if l, err := Listen(soon(t), path); !errors.Is(err, ErrHeld) {
		if err == nil {
			_ = l.Close()
		}
		t.Fatalf("a path whose lock is held: %v, and want ErrHeld", err)
	}
	sameFile(t, "a held lock's refusal", path, was)
	leaves(t, "a held lock's refusal", dir, "app.sock", "app.sock.lock")
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	lockFree(t, "a held lock's refusal", path)
}

// Rule 2: something at the path that is not a socket is refused, and left as it was, with the
// lock let go.
//
// What it costs when this fails: a program pointed at the wrong path removes a file or a folder
// of somebody's and puts its socket there.
func TestSomethingAtThePathThatIsNotASocketIsLeft(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		plant func(t *testing.T, path string)
		check func(t *testing.T, path string)
	}{
		{"a file", func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte("notes"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, path string) {
			t.Helper()
			if b, err := os.ReadFile(path); err != nil || string(b) != "notes" {
				t.Errorf("the file now reads %q (%v)", b, err)
			}
		}},
		{"a folder", func(t *testing.T, path string) {
			t.Helper()
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "kept"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, path string) {
			t.Helper()
			if _, err := os.Stat(filepath.Join(path, "kept")); err != nil {
				t.Errorf("the folder lost what it held: %v", err)
			}
		}},
	}
	ran := 0
	for _, tc := range cases {
		ran++
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		tc.plant(t, path)
		l, err := Listen(soon(t), path)
		if err == nil {
			_ = l.Close()
		}
		if err == nil || errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), "not a socket") {
			t.Errorf("%s at the path: %v, and want a refusal that says it is not a socket", tc.name, err)
		}
		tc.check(t, path)
		leaves(t, tc.name+"'s refusal", dir, "app.sock", "app.sock.lock")
		lockFree(t, tc.name+"'s refusal", path)
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
}

// Rule 3: a socket that answers, a program's that takes no lock, is refused with ErrHeld and
// left listening, with the lock let go.
//
// What it costs when this fails: a program started beside an older one that takes no lock
// removes its socket, and every request reaches the newer one while the older still runs.
func TestASocketThatAnswersIsRefusedWithErrHeld(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	other, err := listenOn(t, unixNet, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	was, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if l, err := Listen(soon(t), path); !errors.Is(err, ErrHeld) {
		if err == nil {
			_ = l.Close()
		}
		t.Fatalf("a socket that answers: %v, and want ErrHeld", err)
	}
	sameFile(t, "an answering socket's refusal", path, was)
	c, err := dial(t, unixNet, path)
	if err != nil {
		t.Fatalf("the other program's socket no longer answers: %v", err)
	}
	_ = c.Close()
	leaves(t, "an answering socket's refusal", dir, "app.sock", "app.sock.lock")
	lockFree(t, "an answering socket's refusal", path)
}

// Rule 4: a socket nothing listens on, with the lock free, refuses the dial as such (ECONNREFUSED).
// With no option it is refused with ErrMaybeBusy and left as it was, the lock let go, on every
// system and kernel: Linux included, where nothing holds the name between rule 4's look and its
// removal. With ReplaceEvenIfBusy it is replaced on every system.
// The listener has the path for its name and its address, is local, answers, and on Close leaves
// only the lock file.
//
// What it costs when this fails: a program that starts beside a live one that takes no lock may
// take its socket from under it, unasked; or a program stopped uncleanly cannot start again, even
// where it said it may, until somebody removes its socket.
func TestASocketARunBeforeLeftIsReplacedOnlyWhereTheCallerSays(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name string
		opts []ListenOption
		// replaced says the socket a run before left is replaced, and not refused with ErrMaybeBusy.
		replaced bool
	}{
		{"no option", nil, false},
		{"ReplaceEvenIfBusy", []ListenOption{ReplaceEvenIfBusy()}, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			runBeforesSocket(t, row.opts, row.replaced)
		})
	}
}

// runBeforesSocket leaves a socket at a path as a run before does, and Listens there with opts: it
// fails t unless the socket is replaced, when replaced says so, and otherwise refused with
// ErrMaybeBusy and left.
func runBeforesSocket(t *testing.T, opts []ListenOption, replaced bool) {
	t.Helper()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	stale(t, path)
	if c, err := dial(t, unixNet, path); !refused(err) {
		if err == nil {
			_ = c.Close()
		}
		t.Fatalf("a dial to a socket nothing listens on says %v, which Listen does not read as refused", err)
	}
	was, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Listen(soon(t), path, opts...)
	if !replaced {
		if !errors.Is(err, ErrMaybeBusy) || !strings.Contains(err.Error(), "ReplaceEvenIfBusy") {
			if err == nil {
				_ = l.Close()
			}
			t.Fatalf("on %s, a socket that refuses a connection: %v, and want ErrMaybeBusy, naming ReplaceEvenIfBusy", runtime.GOOS, err)
		}
		sameFile(t, "a refusal with ErrMaybeBusy", path, was)
		leaves(t, "a refusal with ErrMaybeBusy", dir, "app.sock", "app.sock.lock")
		lockFree(t, "a refusal with ErrMaybeBusy", path)
		return
	}
	if err != nil {
		t.Fatalf("a socket a run before left: %v", err)
	}
	if l.Name() != path || !l.Local() || l.Addr().String() != path {
		t.Errorf("the listener is named %q at %q, local %v, and want %s, local", l.Name(), l.Addr(), l.Local(), path)
	}
	c, err := dial(t, unixNet, path)
	if err != nil {
		t.Fatalf("the new socket does not answer: %v", err)
	}
	_ = c.Close()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("a second Close: %v, and want what the first said", err)
	}
	leaves(t, "Close", dir, "app.sock.lock")
	lockFree(t, "Close", path)
}

// A listener holds its lock until it is closed: a second Listen at the path in the meantime,
// in this process or another, is refused with ErrHeld, and one after Close listens.
//
// What it costs when this fails: a program's lock is let go while it listens, and a second
// start takes its socket.
func TestAListenerHoldsItsLockUntilClosed(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	l, err := Listen(soon(t), path)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := Listen(soon(t), path); !errors.Is(err, ErrHeld) {
		if err == nil {
			_ = again.Close()
		}
		t.Errorf("a second Listen while the first listens: %v, and want ErrHeld", err)
	}
	if ok, err := flock.New(path + ".lock").TryLock(); ok || err != nil {
		t.Errorf("the lock was free while the listener listened (%v)", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := Listen(soon(t), path)
	if err != nil {
		t.Fatalf("a Listen after Close: %v", err)
	}
	_ = after.Close()
}

// Listeners in one process racing for one path take turns: in each round exactly one listens,
// and each other is refused with ErrHeld. After the last Close the folder holds the lock file
// alone.
//
// What it costs when this fails: two starts of one program both say they listen, and every
// visit reaches one while the other waits on a socket with no name.
func TestOneOfManyListenersOnAPathWins(t *testing.T) {
	t.Parallel()
	const racers, rounds = 6, 40
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	ran := 0
	for round := range rounds {
		var wg sync.WaitGroup
		got := make([]*Listener, racers)
		errs := make([]error, racers)
		for i := range racers {
			wg.Go(func() { got[i], errs[i] = Listen(soon(t), path) })
		}
		wg.Wait()
		won := 0
		for i := range racers {
			switch {
			case errs[i] == nil:
				won++
			case !errors.Is(errs[i], ErrHeld):
				t.Errorf("round %d: a racer was refused with %v, and want ErrHeld", round, errs[i])
			}
		}
		if won != 1 {
			t.Errorf("round %d: %d racers listen, and want one", round, won)
		}
		for _, l := range got {
			if l != nil {
				if err := l.Close(); err != nil {
					t.Fatal(err)
				}
			}
		}
		ran++
	}
	if ran != rounds {
		t.Fatalf("ran %d of %d rounds", ran, rounds)
	}
	leaves(t, "the last Close", dir, "app.sock.lock")
}

// A path one byte longer than a Unix socket's address holds on the system is refused before
// anything is made, the lock file included, naming the path and the limit; a path of exactly the
// limit is listened on and answers.
//
// What it costs when this fails: on macOS, a path of 104 to 107 bytes passes the check and fails
// at bind with "invalid argument", and a program launchd keeps alive restarts every ten seconds
// with only that in its log.
func TestAPathLongerThanTheSystemsSocketHoldsIsRefusedByNameBeforeBind(t *testing.T) {
	t.Parallel()
	want, ok := theKernels()[runtime.GOOS]
	if !ok {
		t.Skipf("no sun_path is written down here for %s", runtime.GOOS)
	}
	if sunPath != want {
		t.Fatalf("the limit on %s is %d, and its kernel's is %d", runtime.GOOS, sunPath, want)
	}
	dir := socketDir(t)
	sized := func(n int) string {
		t.Helper()
		p := filepath.Join(dir, strings.Repeat("a", n-len(dir)-len("/.sock"))+".sock")
		if len(p) != n {
			t.Fatalf("a path of %d bytes was asked for, and %d made", n, len(p))
		}
		return p
	}
	long := sized(want + 1)
	_, err := Listen(soon(t), long)
	if err == nil || !strings.Contains(err.Error(), long) || !strings.Contains(err.Error(), fmt.Sprintf("at most %d here", want)) {
		t.Errorf("a path of %d bytes on %s: %v", len(long), runtime.GOOS, err)
	}
	leaves(t, "a long path's refusal", dir)
	fits := sized(want)
	l, err := Listen(soon(t), fits)
	if err != nil {
		t.Fatalf("a path of exactly %d bytes on %s: %v", want, runtime.GOOS, err)
	}
	defer l.Close()
	c, err := dial(t, unixNet, fits)
	if err != nil {
		t.Fatalf("a socket at a path of %d bytes does not answer: %v", want, err)
	}
	_ = c.Close()
}

// theKernels is each system's sun_path less its NUL, as its own headers write it.
func theKernels() map[string]int {
	bsd := 104 - 1
	return map[string]int{
		"linux": 108 - 1, "android": 108 - 1, "windows": 108 - 1,
		"darwin": bsd, "ios": bsd, "freebsd": bsd, "netbsd": bsd, "openbsd": bsd, "dragonfly": bsd,
	}
}

// Rule 4 across processes: a program killed while it listened leaves its socket, and its lock
// goes with it. While it listens, a second program is refused with ErrHeld; once it is killed,
// the dial is refused as one to a socket nothing listens on, and the socket is replaced only with
// ReplaceEvenIfBusy, on every system, being refused with ErrMaybeBusy without it.
//
// What it costs when this fails: a program that crashed cannot start again until somebody
// removes its socket, or a second start beside a live one takes its socket.
func TestASocketWhoseProgramWasKilledIsTakenOver(t *testing.T) {
	// serial: it starts a child process, and a fork while another test holds a lock file open
	// holds that lock until the exec, so a parallel test would find its lock taken.
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	c := startChild(t, dir)
	if said := c.first(t); said != "listening" {
		t.Fatalf("the child said %q", said)
	}
	if l, err := Listen(soon(t), path); !errors.Is(err, ErrHeld) {
		if err == nil {
			_ = l.Close()
		}
		t.Fatalf("a second program beside a listening one: %v, and want ErrHeld", err)
	}
	c.kill(t)
	if conn, err := dial(t, unixNet, path); !refused(err) {
		if err == nil {
			_ = conn.Close()
		}
		t.Fatalf("a dial to the killed program's socket says %v, which Listen does not read as refused", err)
	}
	if l, err := Listen(soon(t), path); !errors.Is(err, ErrMaybeBusy) {
		if err == nil {
			_ = l.Close()
		}
		t.Fatalf("on %s, the killed program's socket with no option: %v, and want ErrMaybeBusy", runtime.GOOS, err)
	}
	l, err := Listen(soon(t), path, ReplaceEvenIfBusy())
	if err != nil {
		t.Fatalf("the killed program's socket was not taken over: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	leaves(t, "Close", dir, "app.sock.lock")
}

// Two programs racing for one path, a hundred times: each time exactly one listens, and the
// other is refused with ErrHeld.
//
// What it costs when this fails: two starts of one program (a doubled unit, a restart while a
// test runs one) both listen, or both are refused and nothing answers.
func TestTwoProgramsRacingForOnePathOneListens(t *testing.T) {
	// serial: it starts child processes, and a fork while another test holds a lock file open
	// holds that lock until the exec, so a parallel test would find its lock taken.
	const rounds = 100
	dir := socketDir(t)
	socketPath(t, dir, "app.sock") // where the children listen, held to the limit
	ran := 0
	for round := range rounds {
		a, b := startChild(t, dir), startChild(t, dir)
		said := []string{a.first(t), b.first(t)}
		listening, held := 0, 0
		for _, s := range said {
			switch {
			case s == "listening":
				listening++
			case strings.HasPrefix(s, "held:"):
				held++
			}
		}
		if listening != 1 || held != 1 {
			t.Fatalf("round %d: the two said %q, and want one listening and one held", round, said)
		}
		codes := []int{a.stop(t), b.stop(t)}
		slices.Sort(codes)
		if !slices.Equal(codes, []int{0, 3}) {
			t.Fatalf("round %d: the two exited %v, and want 0 and 3", round, codes)
		}
		ran++
	}
	if ran != rounds {
		t.Fatalf("ran %d of %d rounds", ran, rounds)
	}
	leaves(t, "the last child's Close", dir, "app.sock.lock")
}

// Listen's waits are the caller's: a ctx with no deadline is refused, naming
// the deadline, for a path and for a TCP address, and nothing is made; and
// the one connection Listen makes to the socket at a path ends with ctx: past
// its deadline, a socket a run before left is not asked, and so not replaced.
//
// What it costs when this fails: Listen waits a span of its own the program
// never chose, as the one second it held did.
func TestListensWaitIsTheCallersCtx(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	for _, addr := range []string{path, "127.0.0.1:0"} {
		l, err := Listen(context.Background(), addr)
		if err == nil {
			_ = l.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "no deadline") {
			t.Errorf("Listen(%s) with a ctx with no deadline: %v, and want it refused, naming the deadline", addr, err)
		}
	}
	leaves(t, "a ctx with no deadline", dir)
	stale(t, path)
	was, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	l, err := Listen(ctx, path)
	if err == nil {
		_ = l.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Listen past its ctx's deadline, at a socket a run before left: %v, and want the deadline passed", err)
	}
	sameFile(t, "a Listen past its deadline", path, was)
	lockFree(t, "a Listen past its deadline", path)
}
