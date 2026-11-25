package listen

import (
	"errors"
	"testing"
)

// An empty address names neither a path nor a TCP address: Listen refuses it with ErrNoAddress
// and makes nothing.
//
// What it costs when this fails: a program whose address setting is empty listens on every
// interface at a port the system picks, reachable from the network, where it meant a socket.
func TestAnEmptyAddressIsRefusedWithErrNoAddress(t *testing.T) {
	t.Parallel()
	l, err := Listen(soon(t), "")
	if !errors.Is(err, ErrNoAddress) {
		if err == nil {
			t.Errorf("an empty address listens at %s", l.Addr())
			_ = l.Close()
		}
		t.Fatalf("an empty address: %v, and want ErrNoAddress", err)
	}
	if l != nil {
		t.Errorf("an empty address's refusal gives a listener too")
	}
}
