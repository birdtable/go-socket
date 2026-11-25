// Package caller is who the caller of a request that came over HTTP is, as a
// front the program trusts wrote it in the request's headers: tailscale
// serve's (TailscaleServe, ServeHeaders), or a login proxy's, in headers the
// program names (LoginProxy). It believes a header only as far as the
// program knows who wrote it. Package peer, which
// says who is at the other end of a connection as the kernel recorded it, is
// how a program knows that; the two packages share no code, and caller
// imports the standard library alone.
//
// It holds too the account tailscaled runs as, read from the socket the
// program names (TailscaledUID), and a Tailscale version read as its two
// numbers (ParseVersion). What a grant means, how a permission is named,
// which Tailscale a program runs on and how serve is set up are the
// program's: caller holds none of them.
//
// It is built on net/http's headers, mime's WordDecoder and
// encoding/json/v2, and on the practice of a front that writes who the
// caller is in a request's headers, as tailscale serve and login proxies do.
// It adds the strict read of those headers, believed only as far as the
// program knows who wrote them. How two logins compare is the program's. The
// program configures the grant's capability name and its cap, a login
// proxy's header names and groups separator, whether a request came through
// its front, and the path of tailscaled's socket.
package caller
