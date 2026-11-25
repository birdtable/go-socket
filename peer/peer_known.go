//go:build linux || darwin || freebsd

package peer

import "fmt"

// errNotUnix is PeerOf's error for a connection that is not a unix socket's,
// checked before the kernel is asked for a peer, so no answer for another kind
// of socket is ever read as one.
var errNotUnix = fmt.Errorf("the connection is not a unix socket's, so the kernel keeps no user for it: %w", ErrCannotSay)

// errNoSocket is PeerOf's error for a connection the kernel cannot be asked
// about at all.
var errNoSocket = fmt.Errorf("the connection is not a socket the kernel can be asked about: %w", ErrCannotSay)
