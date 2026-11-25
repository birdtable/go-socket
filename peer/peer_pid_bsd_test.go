//go:build darwin || freebsd

package peer

import "testing"

// On macOS and FreeBSD PeerOf reads the user alone: the process is not known,
// and its number is 0. peer_pid_linux_test.go holds Linux to its own answer,
// so the test skips on neither.
//
// What it costs when this fails: a program on macOS or FreeBSD takes a number
// the kernel was never asked for as the process at the other end.
func TestPeerOfGivesNoProcessOnMacOSAndFreeBSD(t *testing.T) {
	t.Parallel()
	if p, err := PeerOf(unixPeer(t)); err != nil || p.PIDKnown || p.PID != 0 || p.User != Unix(me()) {
		t.Errorf("a unix peer: %+v %v, want the user %s and no process", p, err, Unix(me()))
	}
}
