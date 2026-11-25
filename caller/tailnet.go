package caller

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ParseVersion is the major and minor number of version, a Tailscale version
// as tailscale and tailscaled write it (1.102.5, or its long form
// 1.102.5-t0123abcd-g4567ef). ok is false unless each of the two is decimal
// digits alone: a sign, a space or a missing minor is no version.
//
// The oldest Tailscale a program runs on is the program's: it compares the
// two numbers with a floor of its own. One fact bears on any floor: a serve
// older than 1.92 neither drops a caller's own HeaderCaps nor writes its own,
// so behind one every grant a program reads may be one the caller wrote.
//
// The version that fact is about is tailscaled's, whose serve proxies the
// requests: the Daemon: line of tailscale version --daemon, or Version in
// tailscale status --json. The tailscale command's own version is not it: a
// new command can stand beside an older tailscaled still running. A program
// that reads both holds the lower to its floor.
func ParseVersion(version string) (major, minor int, ok bool) {
	short, _, _ := strings.Cut(version, "-")
	parts := strings.Split(short, ".")
	if len(parts) < 2 || !digits(parts[0]) || !digits(parts[1]) {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// digits is whether s is one or more decimal digits and nothing else.
func digits(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(c rune) bool { return c < '0' || c > '9' })
}

// ErrNoOwner is TailscaledUID on a system where the account that owns a
// socket is not read, Windows among them. It wraps errors.ErrUnsupported.
var ErrNoOwner = fmt.Errorf("the account that owns a socket is not read on this system: %w", errors.ErrUnsupported)

// TailscaledUID is the account tailscaled runs as: the owner of its socket at
// path, which the program gives, as tailscaled's own socket is where the
// system that runs it put it. No socket at path is nobody, with no error; a
// file there that is not a socket, or one that cannot be read, is nobody and
// an error. A program trusts that uid as a peer itself (package peer's
// Unix). On Windows, and every system but the unix ones, it is ErrNoOwner.
func TailscaledUID(path string) (uid uint32, ok bool, err error) {
	return socketOwner(path)
}
