//go:build linux || darwin || freebsd

package gosocket_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/birdtable/go-socket/listen"
	"github.com/birdtable/go-socket/peer"
)

// Example serves HTTP on a Unix socket of its own that only the user it runs as may reach, and
// calls it once. peer.Listener closes a connection from any other user as it is accepted, before
// a byte is read. The README shows this function as it is, and a test holds the two the same.
// It is built where PeerOf reads the user at the other end of a Unix socket.
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
