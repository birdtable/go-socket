//go:build unix

package caller

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// socketOwner is the uid that owns the socket at path; no file there is
// nobody with no error, and one that is not a socket nobody and an error.
func socketOwner(path string) (uint32, bool, error) {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 0, false, nil
	case err != nil:
		return 0, false, err
	case fi.Mode().Type() != fs.ModeSocket:
		return 0, false, fmt.Errorf("%s is not a socket", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false, fmt.Errorf("the owner of %s is not read on this system", path)
	}
	return st.Uid, true, nil
}
