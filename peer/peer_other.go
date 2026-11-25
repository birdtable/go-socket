//go:build !linux && !darwin && !freebsd && !windows

package peer

import "net"

// peerOf is ErrCannotSay for every connection: the peer of a connection is
// read on Linux, macOS, FreeBSD and Windows alone, so here Principals trusts
// nobody and Listener refuses every connection.
func peerOf(net.Conn) (Peer, error) { return Peer{}, ErrCannotSay }
