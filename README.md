<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/mark/go-socket-banner-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="docs/mark/go-socket-banner-light.svg">
    <img src="docs/mark/go-socket-banner-light.svg" width="327" alt="go-socket: a plug, its pins in coral, then a coral bird in flight and the name">
  </picture>
</p>

# go-socket

Local sockets for Go programs that need to know who is calling.

<p>
  <a href="https://github.com/birdtable/go-socket/releases"><img src="https://img.shields.io/github/v/release/birdtable/go-socket" alt="Latest release"></a>
  <a href="https://pkg.go.dev/github.com/birdtable/go-socket"><img src="https://pkg.go.dev/badge/github.com/birdtable/go-socket.svg" alt="Go reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/birdtable/go-socket" alt="License: Apache-2.0"></a>
</p>

go-socket serves HTTP on a local socket and tells you who is at the other end. It takes the
socket systemd or launchd hands over, or makes its own with the mode and group set before anyone
can connect. It asks the kernel which user is calling and closes the connections you don't
trust before a byte is read. It's for small daemons, agents and local tools that should cost
nothing between visits and answer only the people they mean to.

## Install

```sh
go get github.com/birdtable/go-socket@latest
```

go-socket needs Go 1.27.1 or later. It brings these modules with it, each for one job:

- `github.com/coreos/go-systemd/v22`, whose `activation` package `listen` uses on Linux to take
  the sockets systemd passes.
- `github.com/bored-engineer/go-launchd`, whose `Files` `listen` uses on macOS to take the
  sockets launchd passes.
- `github.com/docker/go-connections`, whose socket options `listen` uses on Unix to give a
  socket its mode before it listens.
- `github.com/gofrs/flock`, for the lock file `listen` holds beside a Unix socket it makes.
- `github.com/Microsoft/go-winio`, for named pipes on Windows: `listen` serves one, and `peer`
  makes and dials them.
- `golang.org/x/sys`, for the system calls `peer` makes to learn who is at the other end: the
  kernel's record of a Unix socket's peer, and on Windows a pipe client's account.

go.mod also names `github.com/birdtable/go-testkit`, a test kit that gives each of go-socket's own
tests an environment of its own. Only the tests import it, so a program that imports go-socket
never builds it.

## Quick start

A server on a Unix socket that only its own user may reach, and one call to it. `peer.Listener`
closes a connection from any other user as it is accepted.

```go
func Example() {
	dir, err := os.MkdirTemp("", "hello")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "hello.sock")

	ctx, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	l, err := listen.Listen(ctx, sock, listen.SocketMode(0o600))
	if err != nil {
		log.Fatal(err)
	}
	me := peer.Principals(peer.Unix(uint32(os.Getuid())))
	l.Listener = peer.Listener(l.Listener, me, nil)

	hello := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "hello")
	})
	o := listen.Options{Grace: 5 * time.Second, ReadHeaderTimeout: 10 * time.Second, KeepAlive: 2 * time.Minute}
	served := make(chan error, 1)
	go func() { served <- listen.Serve(ctx, []*listen.Listener{l}, listen.Same(hello), o) }()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://hello/", nil)
	if err != nil {
		log.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(body))

	stop()
	if err := <-served; err != nil {
		log.Fatal(err)
	}
	// Output: hello
}
```

This is `Example` in [example_test.go](example_test.go), so `go test` runs it and checks what it
prints. It imports `context`, `fmt`, `io`, `log`, `net`, `net/http`, `os`, `path/filepath` and
`time`, plus `listen` and `peer`.

Every timeout is yours to choose. `Listen` refuses a context with no deadline, and `Serve`
refuses a zero span.

Want it as a service? `Example_systemd` in [example_systemd_test.go](example_systemd_test.go)
takes the socket systemd passes and exits after five idle minutes. Started by hand, it makes the
socket itself and serves until interrupted.

## What's inside

| Package | What it does |
|---|---|
| [`listen`](listen/README.md) | Serves HTTP on sockets systemd or launchd pass, or on a socket or named pipe of its own, until the context ends or it has been idle long enough. |
| [`peer`](peer/README.md) | Says which user or account is at the other end of a Unix socket or named pipe, and closes connections from anyone you don't trust. |
| [`caller`](caller/README.md) | Reads who a trusted front, such as `tailscale serve` or a login proxy, says the caller of a request is. |

No package imports another, so use any of them alone or together. Each package README says what
its calls promise, and [pkg.go.dev](https://pkg.go.dev/github.com/birdtable/go-socket) has the
rest.

## Systems

go-socket builds on Linux, macOS, Windows and the BSDs. Each release's tests run on Linux, macOS
and Windows.

- **Linux and macOS** have everything: activated sockets, Unix sockets of your own, and the peer's
  user from the kernel. Linux also gives the peer's process ID.
- **Windows** listens on named pipes instead of Unix sockets, and names the peer by its account
  SID. `caller.TailscaledUID` is unsupported there.
- **FreeBSD** builds and reads the peer's user, but its tests don't run on each release.
- **The other BSDs** build, but can't say who the peer is, so `peer.Listener` refuses every
  connection there.

The promises you can rely on, each with the test that holds it and the systems that test runs on,
are in [api/contract.txt](api/contract.txt).

## Stability

v0: the API may change between minor versions. [CHANGELOG.md](CHANGELOG.md) says what each
release changed.

## Contributing

Issues and pull requests are welcome. [CONTRIBUTING.md](CONTRIBUTING.md) says how to run the
checks.

## Security

Please report vulnerabilities privately, as [SECURITY.md](SECURITY.md) describes, and never in a
public issue.

## License

[Apache-2.0](LICENSE). See also [NOTICE](NOTICE).
