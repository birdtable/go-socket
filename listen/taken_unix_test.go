//go:build unix

package listen

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"sync/atomic"
	"testing"
)

// raceRounds is how many rounds the race with a program that takes no lock runs: with the fix
// taken out,
// darwin-arm64 met "both listen" in 3 to 8 rounds of 2000.
// raceRacers is how many run at once; TestListenAndAProgramThatTakesNoLockNeverBothListen says
// what they meet.
const raceRounds, raceRacers = 4000, 4

// withLockProbe has Listen run f before it opens the lock file ("open") and once it holds the
// lock ("locked"): where only a race reaches, which nobody but a test can time.
func withLockProbe(f func(stage string)) ListenOption {
	return func(l *listen) { l.lockProbe = f }
}

// withBindProbe has Listen run f once it has looked at the path, and removed what rule 4
// replaces, and before it binds; replaced says rule 4 removed a socket.
func withBindProbe(f func(replaced bool)) ListenOption {
	return func(l *listen) { l.bindProbe = f }
}

// withEarlyProbe has Listen run f once the socket listens, before the dial that proves its name
// and before own's first look.
func withEarlyProbe(f func(path string)) ListenOption {
	return func(l *listen) { l.earlyProbe = f }
}

// A name another program takes between Listen's look and its bind is left as it is: Listen
// binds without removing anything first, so its bind is refused and it fails, the other
// program's socket still at the path and still answering, the lock let go. That holds where
// nothing was at the path when Listen looked, and where rule 4 had just removed a run before's
// socket. The other program takes no lock, as one that listens outside Listen does.
//
// What it costs when this fails: a program that starts beside one that takes no lock removes
// its live socket, unasked, and the other program, still running, is cut off from every visitor.
func TestANameTakenSinceListenLookedIsLeft(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name string
		// runBefore leaves a socket at the path first, which rule 4 replaces.
		runBefore bool
	}{
		{"nothing there", false},
		{"a run before's, replaced", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			dir := socketDir(t)
			path := socketPath(t, dir, "app.sock")
			if row.runBefore {
				stale(t, path)
			}
			var other net.Listener
			var otherFile os.FileInfo
			probed := false
			probe := withBindProbe(func(replaced bool) {
				probed = true
				if replaced != row.runBefore {
					t.Errorf("Listen says rule 4 removed a socket: %v, and want %v", replaced, row.runBefore)
				}
				l, err := listenOn(t, unixNet, path)
				if err != nil {
					t.Fatalf("the other program's listen, in the gap: %v", err)
				}
				other = l
				if otherFile, err = os.Lstat(path); err != nil {
					t.Fatal(err)
				}
			})
			l, err := Listen(soon(t), path, ReplaceEvenIfBusy(), probe)
			if !probed {
				t.Fatalf("Listen bound without running its bind probe: %v", err)
			}
			t.Cleanup(func() { _ = other.Close() })
			if err == nil {
				_ = l.Close()
				t.Fatalf("Listen bound at %s, which another program took since it looked, and want it refused", path)
			}
			sameFile(t, "a bind refused", path, otherFile)
			leaves(t, "a bind refused", dir, "app.sock", "app.sock.lock")
			lockFree(t, "a bind refused", path)
			reachesOther(t, path, other)
		})
	}
}

// reachesOther fails t unless a dial to path is accepted by other.
func reachesOther(t *testing.T, path string, other net.Listener) {
	t.Helper()
	accepted := make(chan error, 1)
	go func() {
		c, err := other.Accept()
		if err == nil {
			err = c.Close()
		}
		accepted <- err
	}()
	c, err := dial(t, unixNet, path)
	if err != nil {
		t.Fatalf("the other program's socket does not answer: %v", err)
	}
	_ = c.Close()
	if err := <-accepted; err != nil {
		t.Fatalf("the other program's socket did not accept the dial: %v", err)
	}
}

// The race with a program that takes no lock: in each round another program that takes no
// lock binds and listens at the path while Listen, with no option, takes it. Once Listen holds
// its lock, its probe starts the other program and waits until it runs, so the two go on
// together into Listen's look and bind and the other's. Whatever the order, at most one of them
// listens: Listen never removes the other's live socket. The other counts are logged.
//
// raceRacers racers run at once, each on a folder of its own, each taking every raceRacers'th
// round. With rule 4's refusal taken out of take, the racers meet "both listen" in every run on
// darwin-arm64, 6 to 33 rounds of 4000 a run, where the rounds one after another meet it in about
// 1 run of 5. In the race run of every
// package the racers end within 1.8 s, where one after another took 2.5 s.
//
// What it costs when this fails: as TestANameTakenSinceListenLookedIsLeft, by the race a real
// start meets rather than a probe.
func TestListenAndAProgramThatTakesNoLockNeverBothListen(t *testing.T) {
	t.Parallel()
	for racer := range raceRacers {
		t.Run(fmt.Sprintf("racer %d", racer), func(t *testing.T) {
			t.Parallel()
			dir := socketDir(t)
			var rounds, both, listenWon, otherWon, neither int
			for round := racer; round < raceRounds; round += raceRacers {
				rounds++
				path := socketPath(t, dir, fmt.Sprintf("r%d.sock", round))
				otherErr := make(chan error, 1)
				var other net.Listener
				reached := false
				start := withLockProbe(func(stage string) {
					if stage != "locked" {
						return
					}
					reached = true
					running := make(chan struct{})
					go func() {
						close(running)
						l, err := listenOn(t, unixNet, path)
						other = l
						otherErr <- err
					}()
					<-running
				})
				// replaced says rule 4 removed a socket at the path: it says which branch a round that
				// fails took. The probe only records; it times nothing.
				replaced := false
				record := withBindProbe(func(r bool) { replaced = r })
				l, err := Listen(soon(t), path, start, record)
				if !reached {
					t.Fatalf("round %d: Listen did not reach its lock probe: %v", round, err)
				}
				oerr := <-otherErr
				switch {
				case err == nil && oerr == nil:
					both++
					t.Errorf("round %d: Listen and the other program both listen at %s: the other's socket was removed (rule 4 removed a socket: %v)", round, path, replaced)
				case err == nil:
					listenWon++
				case oerr == nil:
					otherWon++
				default:
					neither++
				}
				if l != nil {
					_ = l.Close()
				}
				if other != nil {
					if ul, ok := other.(*net.UnixListener); ok {
						ul.SetUnlinkOnClose(false)
					}
					_ = other.Close()
				}
				_ = os.Remove(path)
				_ = os.Remove(path + ".lock")
			}
			t.Logf("%d of %d rounds: both listen %d, Listen alone %d, the other alone %d, neither %d", rounds, raceRounds, both, listenWon, otherWon, neither)
		})
	}
}

// The unlink-first race, in two shapes: a run before's socket is at the
// path, and another program that takes no lock unlinks whatever is there, then binds and listens
// (go-connections' NewUnixSocketWithOpts), while Listen, with no option, takes the path. On tmpfs
// the other's socket gets a new inode number; on ext4 or overlayfs it may get the run before's
// again, which os.SameFile cannot tell apart. Rule 4 now removes nothing without
// ReplaceEvenIfBusy, so neither shape can reach a removal and these rounds cannot go red as the
// code stands: they are kept so that a change that removes without the option is caught. In each
// round at most one of the two listens. The folder here is the tests' own (/tmp, which is overlayfs
// in a container and tmpfs or ext4 on a host); on Linux, TestTheUnlinkFirstShapeOnTmpfs runs the
// rounds on /dev/shm too.
//
// What it costs when this fails: a program that starts beside one that takes no lock removes its
// live socket, unasked, and the other program, still running, is cut off from every visitor.
func TestARunBeforesSocketAndAProgramThatUnlinksFirstNeverBothListen(t *testing.T) {
	t.Parallel()
	unlinkFirstRace(t, socketDir(t))
}

// unlinkRounds is how many rounds unlinkFirstRace runs in a folder: with rule 4's refusal taken out
// of take, 172 of 200 met "both listen" on darwin-arm64, in about a quarter of a
// second. 4000 rounds took about 4 seconds a folder, which put the pre-push tier past its budget.
const unlinkRounds = 200

// unlinkFirstRace runs the rounds of TestARunBeforesSocketAndAProgramThatUnlinksFirstNeverBothListen
// in dir.
func unlinkFirstRace(t *testing.T, dir string) {
	t.Helper()
	var both, listenWon, otherWon, neither int
	for round := range unlinkRounds {
		path := socketPath(t, dir, fmt.Sprintf("u%d.sock", round))
		stale(t, path)
		otherErr := make(chan error, 1)
		var other net.Listener
		// spin delays the other program's start, 0 to 200,000 spins spread across the rounds,
		// so its unlink lands before Listen's look, between the look and the bind, and after.
		spin := round * (200_000 / unlinkRounds)
		var spun atomic.Int64
		reached := false
		start := withLockProbe(func(stage string) {
			if stage != "locked" {
				return
			}
			reached = true
			go func() {
				for range spin {
					spun.Add(1)
				}
				if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					otherErr <- err
					return
				}
				l, err := listenOn(t, unixNet, path)
				other = l
				otherErr <- err
			}()
		})
		l, err := Listen(soon(t), path, start)
		if !reached {
			t.Fatalf("round %d: Listen did not reach its lock probe: %v", round, err)
		}
		oerr := <-otherErr
		switch {
		case err == nil && oerr == nil:
			both++
			t.Errorf("round %d: Listen and the other program both listen at %s: Listen removed a socket without the option", round, path)
		case err == nil:
			listenWon++
		case oerr == nil:
			otherWon++
		default:
			neither++
		}
		if l != nil {
			_ = l.Close()
		}
		if other != nil {
			if ul, ok := other.(*net.UnixListener); ok {
				ul.SetUnlinkOnClose(false)
			}
			_ = other.Close()
		}
		_ = os.Remove(path)
		_ = os.Remove(path + ".lock")
	}
	t.Logf("%d rounds in %s: both listen %d, Listen alone %d, the other alone %d, neither %d", unlinkRounds, dir, both, listenWon, otherWon, neither)
}
