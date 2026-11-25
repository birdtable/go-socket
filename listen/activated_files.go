//go:build linux || darwin

package listen

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
)

// distinct refuses a socket name that is empty or asked for twice: one socket
// is handed to one listener.
func distinct(names []string) error {
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if n == "" || seen[n] {
			return fmt.Errorf("the socket name %q is empty or named twice: %w", n, fs.ErrInvalid)
		}
		seen[n] = true
	}
	return nil
}

// listenersOf makes a Listener named name of each file, and closes every
// file. One that is not a stream socket listening is refused, with hint, and
// then every listener made and every file left is closed too.
func listenersOf(files []*os.File, name func(*os.File) string, hint string) ([]*Listener, error) {
	out := make([]*Listener, 0, len(files))
	for i, f := range files {
		fd, n := f.Fd(), name(f)
		l, err := net.FileListener(f)
		cerr := f.Close()
		if err == nil && cerr != nil {
			err = joined(cerr, l.Close())
		}
		if err != nil {
			return nil, joined(fmt.Errorf("descriptor %d (%s) is not a stream socket listening: %s: %w", fd, n, hint, err),
				errors.Join(closeAll(out), closeFiles(files[i+1:])))
		}
		out = append(out, &Listener{Listener: l, name: n, local: l.Addr().Network() == unixNet})
	}
	return out, nil
}

// closeFiles closes each file and says the errors closing them gave, joined, or nil.
func closeFiles(fs []*os.File) error {
	var err error
	for _, f := range fs {
		err = errors.Join(err, f.Close())
	}
	return err
}
