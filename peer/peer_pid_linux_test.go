package peer

import (
	"os"
	"testing"
)

// On Linux PeerOf gives the process at the other end of a unix socket, this
// one, with PIDKnown, and no process for a connection that is no socket.
// peer_pid_bsd_test.go holds macOS and FreeBSD to their own answer, so the
// test skips on neither.
//
// What it costs when this fails: a program on Linux logs the wrong process
// for a peer, or takes a number for a connection the kernel keeps none for.
func TestPeerOfGivesTheProcessAtTheOtherEndOnLinux(t *testing.T) {
	t.Parallel()
	if p, err := PeerOf(unixPeer(t)); err != nil || !p.PIDKnown || p.PID != os.Getpid() {
		t.Errorf("a unix peer's process: %+v %v, want %d, known", p, err, os.Getpid())
	}
	if p, err := PeerOf(fromAddr(gate)); err == nil || p.PIDKnown || p.PID != 0 {
		t.Errorf("a connection that is no socket has the process %+v", p)
	}
}
