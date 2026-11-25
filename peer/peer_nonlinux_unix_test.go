//go:build unix && !linux && !darwin && !freebsd

package peer

import (
	"errors"
	"testing"
)

// Where the kernel's record of a unix peer is not read, the BSDs but FreeBSD,
// PeerOf is ErrCannotSay for a unix socket too, and Principals trusts nobody,
// the test's own user included.
//
// What it costs when this fails: a program on a BSD answers any process on
// the machine as the user it trusts.
func TestPrincipalsTrustNobodyWhereTheKernelIsNotAsked(t *testing.T) {
	t.Parallel()
	p, err := PeerOf(unixPeer(t))
	if !errors.Is(err, ErrCannotSay) || p != (Peer{}) {
		t.Errorf("a unix peer is %+v here, %v", p, err)
	}
	if Principals(Unix(me())).trusts(p.User) {
		t.Errorf("its own user was trusted through the peer %+v", p)
	}
}
