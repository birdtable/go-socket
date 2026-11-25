//go:build unix && !aix && !solaris

package listen

import (
	"os"
	"syscall"
)

// lockFlag is how the lock file is opened: read only, as flock opens it;
// made 0600 when it is not there; never through a symbolic link (O_NOFOLLOW:
// the open fails, and nothing is made where the link points); and without
// waiting on a FIFO put there in place of a file (O_NONBLOCK).
const lockFlag = os.O_CREATE | os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
