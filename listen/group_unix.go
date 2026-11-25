//go:build unix

package listen

import (
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/docker/go-connections/sockets"
)

// toGroup gives a path to group, by name, which is looked up now: a group that
// is not there is refused before anything is made, since a failure once the
// socket is bound leaves the socket where it is (bindAt).
func toGroup(group string) (sockets.SockOption, error) {
	g, err := user.LookupGroup(group)
	if err != nil {
		return nil, fmt.Errorf("the socket's group: %w", err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return nil, fmt.Errorf("the socket's group %q has the id %q: %w", group, g.Gid, err)
	}
	return func(path string) error { return os.Lchown(path, -1, gid) }, nil
}
