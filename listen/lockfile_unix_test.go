//go:build unix

package listen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
)

// linkCases are the links a test puts at the lock file's name on Unix: a symbolic link to a
// name that is not there, which must stay not there, and one to a file, which must be left as
// it was.
func linkCases() []lockCase {
	return []lockCase{
		{"a link to a name that is not there", func(t *testing.T, dir, lock string) {
			t.Helper()
			if err := os.Symlink(filepath.Join(dir, "elsewhere"), lock); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, dir string) {
			t.Helper()
			missing(t, "a link's refusal", filepath.Join(dir, "elsewhere"))
		}},
		{"a link to a file", func(t *testing.T, dir, lock string) {
			t.Helper()
			target := filepath.Join(dir, "elsewhere")
			if err := os.WriteFile(target, []byte("notes"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, lock); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, dir string) {
			t.Helper()
			if b, err := os.ReadFile(filepath.Join(dir, "elsewhere")); err != nil || string(b) != "notes" {
				t.Errorf("the file the link points at reads %q (%v)", b, err)
			}
		}},
	}
}

// The lock file is opened only as itself: opened by lockFlag, a symbolic link at its name to a
// name that is not there fails the open (O_NOFOLLOW), and nothing is made where it points.
//
// What it costs when this fails: a link put at the lock file's name between Listen's look and
// its open has Listen make a file wherever it points, as the program's account.
func TestTheLockFileIsNeverOpenedThroughALink(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	lock, target := filepath.Join(dir, "app.sock.lock"), filepath.Join(dir, "elsewhere")
	if err := os.Symlink(target, lock); err != nil {
		t.Fatal(err)
	}
	f := flock.New(lock, flock.SetFlag(lockFlag), flock.SetPermissions(0o600))
	if ok, err := f.TryLock(); err == nil || ok {
		t.Errorf("the lock was taken through a link (%v, %v), and want the open to fail", ok, err)
	}
	_ = f.Unlock()
	missing(t, "an open through a link", target)
}
