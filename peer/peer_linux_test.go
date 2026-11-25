package peer

import (
	"errors"
	"io/fs"
	"testing"
)

// refusedAsNoPeer holds that PeerOf refused a unix socket with no peer by
// name, as ErrCannotSay, on Linux, where the kernel answers it with the uid
// 4294967295.
func refusedAsNoPeer(t *testing.T, name string, err error) {
	t.Helper()
	if !errors.Is(err, errNoPeer) || !errors.Is(err, ErrCannotSay) {
		t.Errorf("%s: %v, want errNoPeer, ErrCannotSay", name, err)
	}
}

// A peer whose user this process's user namespace does not map reads, through
// SO_PEERCRED, as the overflow uid, which is the namespace's own nobody too.
// Outside the first namespace PeerOf refuses that uid; in it, and for every
// other uid, it reads the user. A file that cannot be read, or does not parse,
// never clears the overflow uid.
//
// What it costs when this fails: a daemon in a rootless container that runs as
// its namespace's nobody and trusts its own uid admits every user outside the
// map, root among them, as itself.
func TestUnmappedRefusesTheOverflowUIDOutsideTheFirstNamespace(t *testing.T) {
	t.Parallel()
	file := func(text string) func() ([]byte, error) {
		return func() ([]byte, error) { return []byte(text), nil }
	}
	missing := func() ([]byte, error) { return nil, fs.ErrNotExist }
	var (
		identity  = file("         0          0 4294967295\n")
		container = file("         0     100000      65536\n")
		twoLines  = file("0 0 1000\n1000 1000 4294966295\n")
		notNumber = file("0 0 4294967295x\n")
		overflow  = file("65534\n")
		moved     = file("1234\n")
		garbled   = file("nobody\n")
	)
	for _, c := range []struct {
		name             string
		uid              uint32
		uidMap, overflow func() ([]byte, error)
		want             bool
	}{
		{"the first namespace's nobody", 65534, identity, overflow, false},
		{"the overflow uid in a container", 65534, container, overflow, true},
		{"another uid in a container", 1000, container, overflow, false},
		{"a map of two lines", 65534, twoLines, overflow, true},
		{"a map that does not parse", 65534, notNumber, overflow, true},
		{"a map that cannot be read", 65534, missing, overflow, true},
		{"a moved overflow uid", 1234, container, moved, true},
		{"the default when the overflow uid moved", 65534, container, moved, false},
		{"an overflow file that does not parse", 65534, container, garbled, true},
		{"an overflow file that cannot be read", 65534, container, missing, true},
	} {
		if got := unmapped(c.uid, c.uidMap, c.overflow); got != c.want {
			t.Errorf("%s: unmapped(%d) = %v, want %v", c.name, c.uid, got, c.want)
		}
	}
}
