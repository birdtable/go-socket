//go:build unix

package listen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A path another program takes once Listen bound, by an unlink and a plain listen of its own
// before Listen proves the path its socket, is refused with ErrMaybeBusy and left: the other's
// socket is at the path and still accepts a dial, and the lock is let go.
//
// What it costs when this fails: Listen takes the other program's socket for its own, and its
// Close removes it, unasked, and the other program, still running, is cut off from every
// visitor.
func TestAPathTakenOnceBoundIsRefusedAndLeft(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	var other net.Listener
	l, err := Listen(soon(t), path, withEarlyProbe(func(p string) {
		if err := os.Remove(p); err != nil {
			t.Error(err)
			return
		}
		o, err := listenOn(t, unixNet, p)
		if err != nil {
			t.Error(err)
			return
		}
		o.(*net.UnixListener).SetUnlinkOnClose(false)
		other = o
		t.Cleanup(func() { _ = o.Close() })
	}))
	if err == nil {
		_ = l.Close()
		t.Fatal("a path another program took once Listen bound was taken for Listen's own")
	}
	if !errors.Is(err, ErrMaybeBusy) {
		t.Errorf("a path another program took once Listen bound: %v, and want ErrMaybeBusy", err)
	}
	if other == nil {
		t.Fatal("the other program did not listen")
	}
	reachesOther(t, path, other)
	leaves(t, "a refusal of a path taken once bound", dir, "app.sock", "app.sock.lock")
	lockFree(t, "a refusal of a path taken once bound", path)
}

// gapRounds is how many rounds TestListenNeverTakesAnotherProgramsSocketForItsOwn runs, gapRacers
// how many run at once; see it for what they cost and what they meet with the proof taken out.
const gapRounds, gapRacers = 4000, 4

// The race with a lockless program: in each round another program that takes no lock, its
// socket made, unlinks the path and binds and listens there, while Listen, with no option, takes
// it. Whatever the order, Close never removes the other's live socket: once Listen answers, a path
// the other listens at, which accepts a dial, is still there after Close, and Close says
// ErrSocketLeft. The rounds come in three kinds, which gapKind deals:
//
//   - timed: the other waits, spinning, until Listen's bind probe says it is about to bind, as
//     unlinkFirstRace's waits on the lock probe, then spins on, its delay spread across these rounds,
//     so its unlink lands before Listen's link, which is then refused, or after it.
//   - watched: from the bind probe on, the other looks at the path until Listen's socket is there,
//     as a program waiting to take the path would, and then at once unlinks and binds: it meets the
//     gap between Listen's link and its proof by the file itself, with no probe in the gap.
//   - met: Listen's early probe, in that gap, starts the other and waits until it has unlinked and
//     bound, the rendezvous of TestAPathTakenOnceBoundIsRefusedAndLeft, so the other's socket is at
//     the path before Listen's first look in every such round, whatever the timing.
//
// gapRacers racers run at once, each on a folder of its own, each taking every gapRacers'th round.
// The timed rounds alone seldom meet the break that makes own's look a plain one: the private
// folder is removed after the proof, not between the link and own's first look, so the gap the
// spread has to land in is a return wide. The met rounds catch p in every run by construction, on
// every system: with p, on darwin-arm64, the row is red in every run, in every met round of every
// run; without p it is green. Alone, the racers end within about a second.
//
// What it costs when this fails: as TestAPathTakenOnceBoundIsRefusedAndLeft, by the race a real
// start meets rather than a probe.
func TestListenNeverTakesAnotherProgramsSocketForItsOwn(t *testing.T) {
	t.Parallel()
	for racer := range gapRacers {
		t.Run(fmt.Sprintf("racer %d", racer), func(t *testing.T) {
			t.Parallel()
			dir := socketDir(t)
			var rounds, hits, refused, listened, otherListened, left int
			var kinds [3]int
			var hitsBy [3]int
			var spun atomic.Int64
			for round := racer; round < gapRounds; round += gapRacers {
				rounds++
				kind := gapKind(round)
				kinds[kind]++
				path := socketPath(t, dir, fmt.Sprintf("g%d.sock", round))
				spin := 0
				if kind == gapTimed {
					spin = round * gapSpin / gapRounds
				}
				done := make(chan struct{})
				var uerr, oerr error
				// go starts the other: Listen's bind probe in a timed or watched round, its early probe
				// in a met one. ended stops a watched round's look once Listen has answered.
				var goAhead, ended atomic.Bool
				// The other program makes its socket first, as one waiting to take the path would, so only
				// its unlink and bind race Listen.
				fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					defer close(done)
					for !goAhead.Load() {
						spun.Add(1)
						spun.Add(1)
					}
					if kind == gapWatched {
						var st syscall.Stat_t
						for syscall.Lstat(path, &st) != nil && !ended.Load() {
							spun.Add(1)
						}
					}
					for range spin {
						spun.Add(1)
					}
					uerr = syscall.Unlink(path)
					if oerr = syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); oerr == nil {
						oerr = syscall.Listen(fd, 8)
					}
				}()
				reached := false
				start := withBindProbe(func(bool) {
					if kind != gapMet {
						reached = true
						goAhead.Store(true)
					}
				})
				meet := withEarlyProbe(func(string) {
					if kind == gapMet {
						reached = true
						goAhead.Store(true)
						<-done
					}
				})
				l, err := Listen(soon(t), path, start, meet)
				ended.Store(true)
				if !reached {
					goAhead.Store(true)
					<-done
					t.Fatalf("round %d: Listen did not reach the probe that starts the other program: %v", round, err)
				}
				<-done
				otherOK := oerr == nil
				if otherOK {
					otherListened++
				}
				if err != nil {
					refused++
				} else {
					listened++
					live := otherOK && accepts(t, path, fd)
					cerr := l.Close()
					if errors.Is(cerr, ErrSocketLeft) {
						left++
					}
					if live {
						if _, serr := os.Lstat(path); errors.Is(serr, fs.ErrNotExist) || !errors.Is(cerr, ErrSocketLeft) {
							hits++
							hitsBy[kind]++
							t.Errorf("round %d, %s, spin %d: the other program's unlink said %v and its socket accepted a dial at %s; then Close said %v, and the path is %v", round, gapKindName(kind), spin, uerr, path, cerr, serr)
						}
					}
				}
				if fd >= 0 {
					_ = syscall.Close(fd)
				}
				_ = os.Remove(path)
				_ = os.Remove(path + ".lock")
			}
			t.Logf("%d of %d rounds (timed %d, spin 0 to %d; watched %d; met %d): Close removed the other's live socket %d (timed %d, watched %d, met %d); Listen refused %d, listened %d; the other listened %d; Close said ErrSocketLeft %d", rounds, gapRounds, kinds[gapTimed], gapSpin, kinds[gapWatched], kinds[gapMet], hits, hitsBy[gapTimed], hitsBy[gapWatched], hitsBy[gapMet], refused, listened, otherListened, left)
		})
	}
}

// gapSpin is the most the other program spins in TestListenNeverTakesAnotherProgramsSocketForItsOwn's
// timed rounds before its unlink.
const gapSpin = 20_000

// The kinds of round of TestListenNeverTakesAnotherProgramsSocketForItsOwn, which says what each
// is.
const (
	gapTimed = iota
	gapWatched
	gapMet
)

// gapKindName is kind's name in the row's lines.
func gapKindName(kind int) string {
	switch kind {
	case gapTimed:
		return "timed"
	case gapWatched:
		return "watched"
	default:
		return "met"
	}
}

// gapKind is round's kind: of each racer's rounds, every eighth is met, and the rest are timed and
// watched by turns.
func gapKind(round int) int {
	switch turn := (round / gapRacers) % 8; {
	case turn == 7:
		return gapMet
	case turn%2 == 0:
		return gapTimed
	default:
		return gapWatched
	}
}

// modeRounds is how many rounds TestListenNeverGivesAnotherProgramsSocketItsMode runs, modeRacers
// at a time; see it for what one costs and what it meets with the private bind taken out.
const modeRounds, modeRacers = 800, 8

// The race of a socket mode set after the bind, with no seam: in each round another program that takes no lock, its
// socket made, looks at the path until a name is there, then unlinks it and binds and listens
// there, while Listen, with SocketMode(0o666), takes it. It is the program the gap costs most: it
// takes the path the moment Listen's socket appears there, so its bind lands just after Listen's,
// which on main was before the mode was set by path. modeRacers rounds run at once, on paths of
// their own, so the machine is busy as a start beside others is, and a round is more often
// preempted where the gap is. Whatever the order, a socket the other bound keeps the mode its
// bind gave it: Listen gives its mode to its own socket alone, at its private name, and only then
// puts it at the path.
//
// With the private bind taken out (the socket bound at the path and its mode set there, as on
// main), this row met it in 42 to 88 of 800 rounds in each of eight runs on darwin-arm64, at a
// load of about 33; one round at a time it met 1 in 200 on a quiet machine, which is why the
// rounds race eight at a time. It runs in about a fifth of a second.
//
// What it costs when this fails: a program that starts beside one that takes no lock opens that
// one's socket to every account, or to the group it gives, and anybody on the machine connects
// to a socket its own program kept to itself.
func TestListenNeverGivesAnotherProgramsSocketItsMode(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	plain := boundMode(t, filepath.Join(dir, "plain.sock"))
	if plain == 0o666 || plain == 0 {
		t.Fatalf("a socket bound here is %v, which is a mode Listen gives: this proves nothing", plain)
	}
	paths := make([]string, modeRounds)
	for round := range paths {
		paths[round] = socketPath(t, dir, fmt.Sprintf("m%d.sock", round))
	}
	var hits, listened, otherListened, saw, ran atomic.Int64
	var wg sync.WaitGroup
	for racer := range modeRacers {
		wg.Go(func() {
			for round := racer; round < modeRounds; round += modeRacers {
				ran.Add(1)
				r := modeRound(t, paths[round], plain)
				for _, c := range []struct {
					n  *atomic.Int64
					is bool
				}{{&hits, r.hit}, {&listened, r.listened}, {&otherListened, r.otherListened}, {&saw, r.saw}} {
					if c.is {
						c.n.Add(1)
					}
				}
			}
		})
	}
	wg.Wait()
	if ran.Load() != modeRounds {
		t.Fatalf("ran %d of %d rounds", ran.Load(), modeRounds)
	}
	t.Logf("%d rounds, %d at a time: the other's socket given another mode %d; the other saw Listen's name %d, listened %d; Listen listened %d", modeRounds, modeRacers, hits.Load(), saw.Load(), otherListened.Load(), listened.Load())
}

// modeRace is what one round of TestListenNeverGivesAnotherProgramsSocketItsMode met.
type modeRace struct {
	hit, listened, otherListened, saw bool
}

// modeRound runs one round of TestListenNeverGivesAnotherProgramsSocketItsMode at path, where a
// socket bound plainly has the mode plain, and fails t if the other program's socket has another.
func modeRound(t *testing.T, path string, plain os.FileMode) modeRace {
	t.Helper()
	var r modeRace
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Error(err)
		return r
	}
	done := make(chan struct{})
	var oerr error
	var over, seen atomic.Bool
	go func() {
		defer close(done)
		for !over.Load() {
			if _, err := os.Lstat(path); err == nil {
				seen.Store(true)
				break
			}
		}
		_ = syscall.Unlink(path)
		if oerr = syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); oerr == nil {
			oerr = syscall.Listen(fd, 8)
		}
	}()
	l, err := Listen(soon(t), path, SocketMode(0o666))
	over.Store(true)
	<-done
	r.listened, r.saw, r.otherListened = err == nil, seen.Load(), oerr == nil
	// Once the other bound, the path is its socket: Listen's link is refused by a name there, and
	// nothing else binds or links at the path.
	if r.otherListened {
		if fi, serr := os.Lstat(path); serr != nil || fi.Mode().Perm() != plain {
			r.hit = true
			t.Errorf("%s: the other program's socket is %v (%v), and its bind gave it %v (Listen said %v)", path, fi, serr, plain, err)
		}
	}
	if l != nil {
		_ = l.Close()
	}
	_ = syscall.Close(fd)
	_ = os.Remove(path)
	_ = os.Remove(path + ".lock")
	return r
}

// boundMode is the mode a socket bound plainly at path has here, under this process's umask; the
// socket is closed and its path removed.
func boundMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// accepts says whether a dial to path is accepted on the listening socket fd, which the other
// program in a race holds: it is the socket at path, live.
func accepts(t *testing.T, path string, fd int) bool {
	t.Helper()
	c, err := dial(t, unixNet, path)
	if err != nil {
		return false
	}
	defer func() { _ = c.Close() }()
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	// Unix's connect queues the connection before it returns, so the accept finds it at once.
	nfd, _, err := syscall.Accept(fd)
	if err != nil {
		return false
	}
	_ = syscall.Close(nfd)
	return true
}

// A client that connects once Listen listens and before its proof is not lost: its connection is
// the caller's first Accept, with what it wrote unread, and two such come in the order they
// connected. The proof's own connection is never one Accept answers.
//
// What it costs when this fails: a client that reaches a restarted program's socket in its first
// instant has its connection closed under it, unread, and its request lost.
func TestAClientThatConnectsBeforeTheProofIsTheFirstAccept(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	sent := []string{"the first client's request", "the second's"}
	var clients []net.Conn
	l, err := Listen(soon(t), path, withEarlyProbe(func(p string) {
		for _, s := range sent {
			c, err := dial(t, unixNet, p)
			if err != nil {
				t.Error(err)
				return
			}
			clients = append(clients, c)
			if _, err := io.WriteString(c, s); err != nil {
				t.Error(err)
			}
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	for _, c := range clients {
		defer func() { _ = c.Close() }()
	}
	if len(clients) != len(sent) {
		t.Fatalf("%d of %d clients connected before the proof", len(clients), len(sent))
	}
	ctx := soon(t)
	for i, want := range sent {
		type accepted struct {
			c   net.Conn
			err error
		}
		got := make(chan accepted, 1)
		go func() {
			c, err := l.Accept()
			got <- accepted{c, err}
		}()
		var a accepted
		select {
		case a = <-got:
		case <-ctx.Done():
			// Closing the listener lets the Accept go: nothing came, so the client was lost.
			_ = l.Close()
			t.Fatalf("Accept %d answered nothing before the test's deadline: the early client was lost", i)
		}
		c, err := a.c, a.err
		if err != nil {
			t.Fatalf("Accept %d: %v", i, err)
		}
		if got := c.LocalAddr().String(); got != path {
			t.Errorf("Accept %d's connection is at %s, and want the path %s", i, got, path)
		}
		b := make([]byte, len(want))
		_, err = io.ReadFull(c, b)
		_ = c.Close()
		if err != nil || string(b) != want {
			t.Errorf("Accept %d reads %q (%v), and want %q", i, b, err, want)
		}
	}
}

// withProvedProbe has Listen run f once the proof's dial is accepted on the socket bound and
// before the proof's second look, on Unix.
func withProvedProbe(f func(path string)) ListenOption {
	return func(l *listen) { l.provedProbe = f }
}

// A path another program takes after the proof's dial is accepted, by an unlink and a plain listen
// of its own, is refused by the proof's second look with ErrMaybeBusy and left: the other's socket
// is at the path and still accepts a dial, and the lock is let go.
//
// What it costs when this fails: Listen answers a listener no client reaches, the path being the
// other program's socket, and the caller serves nothing and is not told.
func TestAPathTakenAfterTheProofsDialIsRefusedAndLeft(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	path := socketPath(t, dir, "app.sock")
	var other net.Listener
	l, err := Listen(soon(t), path, withProvedProbe(func(p string) {
		if err := os.Remove(p); err != nil {
			t.Error(err)
			return
		}
		o, err := listenOn(t, unixNet, p)
		if err != nil {
			t.Error(err)
			return
		}
		o.(*net.UnixListener).SetUnlinkOnClose(false)
		other = o
		t.Cleanup(func() { _ = o.Close() })
	}))
	if err == nil {
		_ = l.Close()
		t.Fatal("a path another program took after the proof's dial was taken for Listen's own")
	}
	if !errors.Is(err, ErrMaybeBusy) {
		t.Errorf("a path another program took after the proof's dial: %v, and want ErrMaybeBusy", err)
	}
	if other == nil {
		t.Fatal("the other program did not listen")
	}
	reachesOther(t, path, other)
	leaves(t, "a refusal of a path taken after the proof's dial", dir, "app.sock", "app.sock.lock")
	lockFree(t, "a refusal of a path taken after the proof's dial", path)
}

// A path another program unlinks once Listen bound, and binds nothing at yet, is refused at either
// of the proof's looks with an error that wraps ErrMaybeBusy and not fs.ErrNotExist; nothing is
// removed, the folder holds the lock file alone, and the lock is let go.
//
// What it costs when this fails: Listen fails with a bare "lstat ...: no such file or directory",
// which reads as a folder that is not there, and a caller that matches ErrMaybeBusy to tell another
// program taking the path from a broken start misses it: more than half of the failed proofs in
// the race's runs were this one.
func TestAPathUnlinkedOnceBoundIsRefusedWithErrMaybeBusy(t *testing.T) {
	t.Parallel()
	for _, look := range []struct {
		name string
		at   func(f func(path string)) ListenOption
	}{
		{"first look", withEarlyProbe},
		{"second look", withProvedProbe},
	} {
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		l, err := Listen(soon(t), path, look.at(func(p string) {
			if err := os.Remove(p); err != nil {
				t.Error(err)
			}
		}))
		if err == nil {
			_ = l.Close()
			t.Errorf("%s: a path another program unlinked once Listen bound was taken for Listen's own", look.name)
			continue
		}
		if !errors.Is(err, ErrMaybeBusy) || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: a path another program unlinked once Listen bound: %v, and want ErrMaybeBusy, not fs.ErrNotExist", look.name, err)
		}
		what := "a refusal of a path unlinked at the proof's " + look.name
		leaves(t, what, dir, "app.sock.lock")
		lockFree(t, what, path)
	}
}

// A SocketMode its owner may not write is refused with an error that wraps fs.ErrInvalid before
// anything is made, no lock file and no socket; one its owner may write, even with no read, listens.
//
// What it costs when this fails: the proof's dial is refused for permission once the socket is
// bound, so Listen fails and leaves its socket at the path, and every start after it is refused
// there until someone removes the socket by hand.
func TestASocketModeItsOwnerCannotWriteIsRefusedBeforeAnythingIsMade(t *testing.T) {
	t.Parallel()
	refused := []os.FileMode{0, 0o040, 0o060, 0o400, 0o440, 0o460, 0o577}
	ran := 0
	for _, mode := range refused {
		ran++
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		what := fmt.Sprintf("SocketMode(%#o)", mode)
		l, err := Listen(soon(t), path, SocketMode(mode))
		if err == nil {
			_ = l.Close()
			t.Errorf("%s listens, and want it refused", what)
			continue
		}
		if !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("%s: %v, and want fs.ErrInvalid", what, err)
		}
		leaves(t, what, dir)
	}
	if ran != len(refused) {
		t.Fatalf("ran %d of %d modes", ran, len(refused))
	}
	for _, mode := range []os.FileMode{0o200, 0o620} {
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		l, err := Listen(soon(t), path, SocketMode(mode))
		if err != nil {
			t.Errorf("SocketMode(%#o): %v, and want it to listen", mode, err)
			continue
		}
		if err := l.Close(); err != nil {
			t.Errorf("SocketMode(%#o)'s Close: %v", mode, err)
		}
	}
}

// A Listen that fails its own proof for its own reason, its ctx ended by the proof's dial, leaves
// no socket of its own at the path: the folder holds the lock file alone, and the next Listen there
// with no option listens. That holds for a ctx already past its deadline at the call, and for one
// that passes it between the bind and the dial. Its error is the dial's own, which wraps the ctx's,
// and not ErrMaybeBusy, as nothing refusing is left. A third row is the guard: another program's
// socket taken in at the path in that gap, the ctx ended, is left, and still accepts a dial, as a
// socket not proved Listen's is never removed, and that failure wraps ErrMaybeBusy.
//
// What it costs when this fails: a start whose ctx ran out leaves its dead socket at the path, and
// every start after it without ReplaceEvenIfBusy answers ErrMaybeBusy until someone removes the
// file by hand. Or, the guard row, a start whose
// ctx ran out removes another program's live socket, and that program is cut off from every visitor.
// Or a start whose ctx ran out says ErrMaybeBusy of a path it emptied, and its caller, following
// the text, gives ReplaceEvenIfBusy, the one option that can cost another program its live
// socket.
func TestAListenWhoseCtxEndsInTheProofLeavesNoSocketOfItsOwn(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name string
		// late is how far past now the ctx's deadline is: below zero, past at the call.
		late time.Duration
		// other has another program take the path once Listen bound, before the proof's dial.
		other bool
	}{
		{"a ctx past its deadline at the call", -time.Second, false},
		{"a ctx that passes its deadline between the bind and the dial", 200 * time.Millisecond, false},
		{"another program's socket at the path, the ctx past", -time.Second, true},
	}
	ran := 0
	for _, row := range rows {
		ran++
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(row.late))
		var other net.Listener
		l, err := Listen(ctx, path, withEarlyProbe(func(p string) {
			<-ctx.Done()
			if !row.other {
				return
			}
			if err := os.Remove(p); err != nil {
				t.Error(err)
				return
			}
			o, err := listenOn(t, unixNet, p)
			if err != nil {
				t.Error(err)
				return
			}
			o.(*net.UnixListener).SetUnlinkOnClose(false)
			other = o
			t.Cleanup(func() { _ = o.Close() })
		}))
		cancel()
		if err == nil {
			_ = l.Close()
			t.Errorf("%s: Listen listens, and want it to fail", row.name)
			continue
		}
		// The dial's own error, the ctx's, in every row; ErrMaybeBusy only where the
		// path is left, another program's socket there.
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: %v, and want the dial's own error, which wraps %v", row.name, err, context.DeadlineExceeded)
		}
		if errors.Is(err, ErrMaybeBusy) != row.other {
			t.Errorf("%s: %v, and want ErrMaybeBusy %v: the path is left %v", row.name, err, row.other, row.other)
		}
		lockFree(t, row.name, path)
		if row.other {
			if other == nil {
				t.Fatalf("%s: the other program did not listen", row.name)
			}
			leaves(t, row.name, dir, "app.sock", "app.sock.lock")
			reachesOther(t, path, other)
			continue
		}
		leaves(t, row.name, dir, "app.sock.lock")
		next, err := Listen(soon(t), path)
		if err != nil {
			t.Errorf("%s: the next Listen there, with no option: %v, and want it to listen", row.name, err)
			continue
		}
		if err := next.Close(); err != nil {
			t.Errorf("%s: the next Listen's Close: %v", row.name, err)
		}
	}
	if ran != len(rows) {
		t.Fatalf("ran %d of %d rows", ran, len(rows))
	}
}
