//go:build !unix

package caller

import (
	"errors"
	"path/filepath"
	"testing"
)

// Off the unix systems the owner of a socket is not read: TailscaledUID is
// ErrNoOwner, which is errors.ErrUnsupported, and names nobody.
//
// What it costs when this fails: a program on Windows trusts an account it
// never read, or cannot tell unsupported from a socket that is not there.
func TestTailscaledUIDIsUnsupportedOffUnix(t *testing.T) {
	t.Parallel()
	uid, ok, err := TailscaledUID(filepath.Join(t.TempDir(), "a.sock"))
	if !errors.Is(err, ErrNoOwner) || !errors.Is(err, errors.ErrUnsupported) || ok || uid != 0 {
		t.Fatalf("held 0 of 1: %d %v %v", uid, ok, err)
	}
}
