//go:build !windows

package peer

import (
	"errors"
	"testing"
)

// Off Windows, ListenPipe and DialPipe are ErrCannotSay, which is
// errors.ErrUnsupported, and hand back nothing.
//
// What it costs when this fails: a program built for Linux, macOS or a BSD
// takes a named pipe for something it has, or cannot tell an unsupported
// system from a failed listen and retries one that never comes.
func TestListenPipeAndDialPipeAreUnsupportedOffWindows(t *testing.T) {
	t.Parallel()
	l, err := ListenPipe(`\\.\pipe\peer-test`, "S-1-5-18")
	if l != nil || !errors.Is(err, ErrCannotSay) || !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("ListenPipe = %v, %v, want no listener and ErrCannotSay", l, err)
	}
	c, err := DialPipe(t.Context(), `\\.\pipe\peer-test`)
	if c != nil || !errors.Is(err, ErrCannotSay) || !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("DialPipe = %v, %v, want no connection and ErrCannotSay", c, err)
	}
}
