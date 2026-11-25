//go:build !linux

package listen

import (
	"errors"
	"os"
	"strconv"
	"testing"
)

// Off Linux systemd's variables are not read, even ones that name this
// process: ErrNotActivated, which wraps errors.ErrUnsupported. On macOS the
// socket comes from launchd alone, which did not start this test.
//
// What it costs when this fails: a program on macOS, Windows or a BSD that
// inherits the variables takes its fd 3, whatever it is, for a listener.
func TestOffLinuxNothingIsActivated(t *testing.T) {
	// serial: it sets LISTEN_FDS and LISTEN_PID with t.Setenv, which a parallel test may not.
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "1")
	t.Setenv("LISTEN_FDNAMES", "web")
	ls, err := Activated("web")
	if !errors.Is(err, ErrNotActivated) || !errors.Is(err, errors.ErrUnsupported) || ls != nil {
		t.Errorf("off Linux: %d listeners, %v, and want none and ErrNotActivated", len(ls), err)
	}
}
