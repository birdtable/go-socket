package gosocket_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/birdtable/go-socket/listen"
	"github.com/birdtable/go-socket/peer"
)

// Example_systemd is a server on a Unix socket that one user alone may reach: the user the gate
// in front of it runs as. Started by its socket, it takes the socket systemd or launchd passes
// under the name hello (the unit's FileDescriptorName=hello, or the job's Sockets key) and leaves
// after five idle minutes; started by hand, it makes the socket itself and serves until it is
// interrupted. It has no Output line, so go test compiles it and does not run it.
func Example_systemd() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	o := listen.Options{
		Idle:              listen.NewIdle(5*time.Minute, time.Second),
		Grace:             5 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		KeepAlive:         2 * time.Minute,
	}
	ls, err := listen.Activated("hello")
	if errors.Is(err, listen.ErrNotActivated) {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		l, err := listen.Listen(lctx, "/run/hello/hello.sock", listen.SocketMode(0o660), listen.SocketGroup("hello"))
		cancel()
		if err != nil {
			log.Fatal(err)
		}
		ls, o.Idle = []*listen.Listener{l}, nil
	} else if err != nil {
		log.Fatal(err)
	}

	gate := peer.Principals(peer.Unix(1000)) // the user the gate runs as
	for i := range ls {
		ls[i].Listener = peer.Listener(ls[i].Listener, gate, nil)
	}

	hello := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "hello")
	})
	if err := listen.Serve(ctx, ls, listen.Same(hello), o); err != nil {
		log.Fatal(err)
	}
}
