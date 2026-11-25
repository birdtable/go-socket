//go:build unix

package listen

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

// missing fails t unless nothing is at p.
func missing(t *testing.T, what, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after %s, %s is there (%v), and want nothing made there", what, p, err)
	}
}

// lockCase is something put at the lock file's name, and what must hold of the socket's folder
// once Listen refuses it.
type lockCase struct {
	name  string
	plant func(t *testing.T, dir, lock string)
	check func(t *testing.T, dir string)
}

// The lock file's name must be a plain file or nothing: a link there (linkCases says which
// each system makes) and a folder are refused with ErrLockNotAFile and left as they are, nothing
// is made where a link points, and no socket is made.
//
// What it costs when this fails: anyone who may write the socket's folder points the lock file's
// name at a file of the program's account, and Listen makes it, or locks that file, for them.
func TestALockFileNameThatIsNotAPlainFileIsRefused(t *testing.T) {
	t.Parallel()
	cases := append(linkCases(), lockCase{"a folder", func(t *testing.T, _, lock string) {
		t.Helper()
		if err := os.Mkdir(lock, 0o700); err != nil {
			t.Fatal(err)
		}
	}, func(*testing.T, string) {}})
	ran := 0
	for _, tc := range cases {
		ran++
		dir := socketDir(t)
		path := socketPath(t, dir, "app.sock")
		tc.plant(t, dir, path+".lock")
		was, err := os.Lstat(path + ".lock")
		if err != nil {
			t.Fatal(err)
		}
		l, err := Listen(soon(t), path)
		if !errors.Is(err, ErrLockNotAFile) {
			if err == nil {
				_ = l.Close()
			}
			t.Errorf("%s at the lock file's name: %v, and want ErrLockNotAFile", tc.name, err)
		}
		sameFile(t, tc.name+"'s refusal", path+".lock", was)
		missing(t, tc.name+"'s refusal", path)
		tc.check(t, dir)
	}
	if ran != len(cases) || ran < 2 {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
}
