package listen

import (
	"os"
	"testing"
)

// TestARunBeforesSocketAndAProgramThatUnlinksFirstNeverBothListen's rounds on /dev/shm, tmpfs on
// every Linux, which gives the other program's socket a new inode number: the race's first
// shape. With no option Listen removes nothing, so at most one of
// the two listens in each round.
//
// What it costs when this fails: a program that starts beside one that takes no lock removes its
// live socket, unasked, and the other program, still running, is cut off from every visitor.
func TestTheUnlinkFirstShapeOnTmpfs(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("/dev/shm", "listen")
	if err != nil {
		t.Fatalf("a folder on /dev/shm, tmpfs: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	unlinkFirstRace(t, dir)
}
