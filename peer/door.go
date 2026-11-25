// Package peer is who is at the other end of a program's connection, as the
// kernel says: the user, and on Linux the process, on a unix socket, and the
// account on a Windows named pipe that only the accounts a program lists may
// open (PeerOf, ListenPipe, DialPipe), and a listener that closes every
// connection whose peer is not among the principals a program trusts as it is
// accepted (Principals, Unix, Listener). Nothing else a request says decides
// it: no login, password or account is kept. A program passes in the
// principals it trusts. Who tailscale serve says the caller is is package
// caller's.
//
// It is built on golang.org/x/sys/unix's GetsockoptUcred and
// GetsockoptXucred, and on Windows on Microsoft/go-winio's named pipe and
// golang.org/x/sys/windows' token and security calls. It adds what they
// lack: the checks of the kernel's answer that x/sys leaves to its caller,
// the client's account on a pipe, read by impersonation, access lists set by
// SID with the SIDs that name every caller refused, and the refusal of a peer
// before a byte is read. The program configures the principals it trusts,
// the SIDs it lists, the pipe's name, and what it is told of each refusal.
package peer
