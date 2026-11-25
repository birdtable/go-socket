package listen

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"testing"
)

// asked is the ways a program asks for its sockets on this system: by a name
// everywhere, and with no name where the system passes sockets unnamed too,
// which every system but macOS does or refuses alike.
func asked() [][]string {
	if runtime.GOOS == "darwin" {
		return [][]string{{"web"}}
	}
	return [][]string{nil, {"web"}}
}

// A process nothing started by its socket is told so, with ErrNotActivated,
// which wraps errors.ErrUnsupported, and given no listener.
//
// What it costs when this fails: a program run by hand waits on descriptors
// it was never given, or cannot tell "not started by a socket" from a fault
// and falls back to Listen on neither.
func TestAProcessNotStartedByItsSocketIsToldSo(t *testing.T) {
	// serial: it sets LISTEN_FDS and LISTEN_PID with t.Setenv, which a parallel test may not.
	t.Setenv("LISTEN_PID", "")
	t.Setenv("LISTEN_FDS", "")
	for _, names := range asked() {
		ls, err := Activated(names...)
		if !errors.Is(err, ErrNotActivated) || !errors.Is(err, errors.ErrUnsupported) || ls != nil {
			t.Errorf("not started by a socket, asked for %q: %d listeners, %v, and want none and ErrNotActivated", names, len(ls), err)
		}
	}
}

// Descriptors passed to another process are not this one's, whatever
// LISTEN_FDS says: a program run from a socket-started one inherits the
// variables, and would take descriptors that are something else. The
// variables are set again for each way of asking, as a call clears them.
//
// What it costs when this fails: a child takes its parent's fd 3, a file or
// a pipe, for a listener, and fails strangely or closes it under the parent.
func TestDescriptorsPassedToAnotherProcessAreNotTaken(t *testing.T) {
	// serial: it sets LISTEN_FDS and LISTEN_PID with t.Setenv, which a parallel test may not.
	t.Setenv("LISTEN_FDNAMES", "")
	for _, names := range asked() {
		t.Setenv("LISTEN_PID", strconv.Itoa(os.Getppid()))
		t.Setenv("LISTEN_FDS", "1")
		if _, err := Activated(names...); !errors.Is(err, ErrNotActivated) {
			t.Errorf("another process's descriptors were taken, asked for %q: %v", names, err)
		}
		t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
		t.Setenv("LISTEN_FDS", "0")
		if _, err := Activated(names...); !errors.Is(err, ErrNotActivated) {
			t.Errorf("no descriptors read as some, asked for %q: %v", names, err)
		}
	}
}
