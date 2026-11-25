//go:build unix

package listen

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"

	"github.com/docker/go-connections/sockets"
)

// access is what the socket is given between its bind and its listen: its
// mode, and its group when one is asked, looked up here, before anything is
// made. A mode its owner may not write is refused then too: own dials the
// socket to prove its path, and a connect needs write, so the proof would
// fail once bound and leave the socket at the path.
func access(o listen) ([]sockets.SockOption, error) {
	if o.mode&0o200 == 0 {
		return nil, fmt.Errorf("SocketMode(%#o): the socket's owner must be able to write it (0200), since Listen dials it to prove its path and a connect needs write, so nothing is made: %w", uint32(o.mode), fs.ErrInvalid)
	}
	given := []sockets.SockOption{sockets.WithChmod(o.mode)}
	if o.group != "" {
		give, err := toGroup(o.group)
		if err != nil {
			return nil, err
		}
		given = append(given, give)
	}
	return given, nil
}

// refused says whether a connection was refused as one to a socket nothing
// listens on is.
func refused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
