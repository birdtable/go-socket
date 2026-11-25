//go:build linux || darwin

package listen

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"syscall"
	"testing"
)

// A socket name that is empty or asked for twice is refused with
// fs.ErrInvalid, before systemd's variables are read or launchd is asked:
// the variables are still set after the refusal.
//
// What it costs when this fails: one socket is handed to two listeners,
// which then take each other's connections, or an empty name takes a socket
// systemd passed unnamed.
func TestASocketNameEmptyOrNamedTwiceIsRefused(t *testing.T) {
	// serial: it sets LISTEN_FDS and LISTEN_PID with t.Setenv, which a parallel test may not.
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "0")
	for _, names := range [][]string{{""}, {"web", "web"}, {"web", ""}, {"web", "admin", "web"}} {
		ls, err := Activated(names...)
		if !errors.Is(err, fs.ErrInvalid) || errors.Is(err, ErrNotActivated) || ls != nil {
			t.Errorf("asked for %q: %d listeners, %v, and want none and fs.ErrInvalid", names, len(ls), err)
		}
		if os.Getenv("LISTEN_FDS") != "0" {
			t.Fatalf("asked for %q: the variables were read before the names were refused", names)
		}
	}
}

// heldSockets is the address of each named Unix socket this process holds a
// descriptor of, among its first 1024.
func heldSockets() []string {
	var held []string
	for fd := range 1024 {
		if sa, err := syscall.Getsockname(fd); err == nil {
			if u, ok := sa.(*syscall.SockaddrUnix); ok && u.Name != "" {
				held = append(held, strconv.Itoa(fd)+" "+u.Name)
			}
		}
	}
	return held
}
