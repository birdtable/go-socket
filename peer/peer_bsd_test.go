//go:build darwin || freebsd

package peer

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// refusedAsNoPeer holds nothing more on macOS and FreeBSD than that PeerOf
// refused: the kernel refuses a unix socket with no peer itself, with its own
// errno (EINVAL, ENOTCONN).
func refusedAsNoPeer(*testing.T, string, error) {}

// xucredUser reads the user from an answer of version 0 with at least one
// group, and refuses by name, errXucred, an answer of another version or with
// no group: the kernel never gives those for a peer, and a short answer, whose
// unwritten bytes x/sys leaves zero, is one with no group and uid 0.
//
// What it costs when this fails: an answer the kernel cut short, or wrote in a
// layout this code does not know, reads as root, and a door that trusts root
// lets the caller in.
func TestXucredUserRefusesAnAnswerItDoesNotRead(t *testing.T) {
	t.Parallel()
	if uid, err := xucredUser(&unix.Xucred{Version: 0, Uid: 501, Ngroups: 1}); err != nil || uid != 501 {
		t.Errorf("version 0 with one group: %d %v, want 501", uid, err)
	}
	refusals := []unix.Xucred{
		{},
		{Version: 0, Uid: 501, Ngroups: 0},
		{Version: 0, Uid: 0, Ngroups: -1},
		{Version: 1, Uid: 501, Ngroups: 1},
		{Version: 1<<32 - 1, Uid: 501, Ngroups: 16},
	}
	refused := 0
	for _, x := range refusals {
		if uid, err := xucredUser(&x); !errors.Is(err, errXucred) || uid != 0 {
			t.Errorf("%+v: the user %d and %v, want errXucred", x, uid, err)
		}
		refused++
	}
	if refused != len(refusals) {
		t.Fatalf("held %d of %d answers", refused, len(refusals))
	}
}
