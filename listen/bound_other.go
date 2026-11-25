//go:build unix && !linux && !darwin

package listen

// noneBound is nil on the BSDs, and on any Unix but Linux and darwin: no run
// has measured what a datagram connection answers there (FreeBSD's is
// unmeasured, as no run of ours reaches it), so rule 4 replaces
// a socket that refuses only with ReplaceEvenIfBusy, and asks nothing more:
// the caller gives it knowing a program that takes no lock may hold the
// socket.
func noneBound(string) error { return nil }
