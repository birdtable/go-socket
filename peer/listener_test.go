//go:build linux || darwin || freebsd

package peer

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// counted is a listener whose connections count the bytes the server reads
// from them.
type counted struct {
	net.Listener
	read *atomic.Int64
}

func (l counted) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return countedConn{c, l.read}, nil
}

type countedConn struct {
	net.Conn
	read *atomic.Int64
}

func (c countedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.read.Add(int64(n))
	return n, err
}

// SyscallConn is the socket's own, so the kernel can be asked about it.
func (c countedConn) SyscallConn() (syscall.RawConn, error) {
	return c.Conn.(syscall.Conn).SyscallConn()
}

// refusal is one call of a Listener's refused.
type refusal struct {
	principal string
	known     bool
}

// gatedRun listens on network, gates the listener with p, connects to it,
// writes a request, and reads until the server closes or a second passes,
// then closes, so a server waiting on more of it is not held. It returns
// whether Accept handed the connection on, the bytes the server read, and the
// refusals, all read once Accept has returned.
func gatedRun(t *testing.T, network string, p Peers) (accepted bool, read int64, refused []refusal) {
	t.Helper()
	addr := "127.0.0.1:0"
	if network == "unix" {
		dir, err := os.MkdirTemp("/tmp", "peer")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		addr = filepath.Join(dir, "s.sock")
	}
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), network, addr)
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	var mu sync.Mutex
	g := Listener(counted{l, &n}, p, func(principal string, known bool) {
		mu.Lock()
		defer mu.Unlock()
		refused = append(refused, refusal{principal, known})
	})
	done := make(chan net.Conn, 1)
	go func() {
		c, err := g.Accept()
		if err == nil {
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _ = io.ReadAll(io.LimitReader(c, 5))
			_ = c.Close()
		}
		done <- c
	}()
	var d net.Dialer
	c, err := d.DialContext(context.Background(), network, l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("hello"))
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	_, _ = io.ReadAll(c)
	_ = c.Close()
	_ = l.Close()
	got := <-done
	mu.Lock()
	defer mu.Unlock()
	return got != nil, n.Load(), refused
}

// Listener closes a connection whose peer it does not trust before the server
// reads a byte of it, and tells refused, once, the peer's principal, read; a
// trusted peer's connection is handed on and read.
//
// What it costs when this fails: a stranger on the machine reaches the
// program's parser, or its whole handler, before the door refuses them, or the
// person who runs the program is refused at their own socket.
func TestListenerRefusesAStrangerBeforeAByteIsRead(t *testing.T) {
	t.Parallel()
	accepted, read, refused := gatedRun(t, "unix", Principals(Unix(me()+1)))
	if accepted || read != 0 {
		t.Errorf("a stranger's connection was handed on (%v) or read: %d bytes", accepted, read)
	}
	if len(refused) != 1 || refused[0] != (refusal{Unix(me()), true}) {
		t.Errorf("refused was told %v, want the stranger's principal %s, read, once", refused, Unix(me()))
	}
	// The negative half: the principal it trusts is handed on, and read.
	accepted, read, refused = gatedRun(t, "unix", Principals(Unix(me())))
	if !accepted || read != 5 || len(refused) != 0 {
		t.Errorf("the trusted principal: handed on %v, read %d bytes, refused %v", accepted, read, refused)
	}
}

// A peer PeerOf cannot read, a TCP one on loopback, is refused at accept even
// by a Listener that trusts the test's own user, and refused is told no
// principal was known: being local is never an identity.
//
// What it costs when this fails: a TCP connection from anyone on the machine
// passes the door as the user it trusts, or because nothing said no.
func TestListenerRefusesAPeerWhoseUserCannotBeRead(t *testing.T) {
	t.Parallel()
	accepted, read, refused := gatedRun(t, "tcp", Principals(Unix(me())))
	if accepted || read != 0 || len(refused) != 1 || refused[0] != (refusal{"", false}) {
		t.Errorf("a TCP peer: handed on %v, read %d bytes, refused %v", accepted, read, refused)
	}
}

// Principals with no principal, and the zero Peers, admit nobody, the test's
// own user included, and refused is told the peer that was turned away.
//
// What it costs when this fails: a program that was given an empty list of
// users, from a setting left blank, lets every account on the machine in.
func TestNoPrincipalAdmitsNobody(t *testing.T) {
	t.Parallel()
	for _, p := range []Peers{Principals(), {}} {
		accepted, read, refused := gatedRun(t, "unix", p)
		if accepted || read != 0 || len(refused) != 1 || refused[0] != (refusal{Unix(me()), true}) {
			t.Errorf("%v: handed on %v, read %d bytes, refused %v", p, accepted, read, refused)
		}
	}
}

// Accept returns the listener's own error once it is closed, and nothing
// else, so a server's loop ends.
//
// What it costs when this fails: a server whose listener is closed spins, or
// takes a refusal for the end of its listener.
func TestListenerReturnsTheListenersErrorWhenItCloses(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	if c, err := Listener(l, Principals(Unix(me())), nil).Accept(); c != nil || !errors.Is(err, net.ErrClosed) {
		t.Errorf("a closed listener's Accept gave %v and %v, want net.ErrClosed", c, err)
	}
}

// closeFails is a connection that is no socket and will not close.
type closeFails struct{ net.Conn }

func (closeFails) Close() error { return errors.New("the file is not the program's to close") }

// closes is a connection that is no socket and closes.
type closes struct{ net.Conn }

func (closes) Close() error { return nil }

// once is a listener that hands out c, then is closed.
type once struct {
	net.Listener
	c    net.Conn
	done bool
}

func (l *once) Accept() (net.Conn, error) {
	if l.done {
		return nil, net.ErrClosed
	}
	l.done = true
	return l.c, nil
}

// Accept returns the error of closing a refused connection that will not
// close, after refused is told of it, and does not go on as if it were shut;
// the negative half is the closed listener's own error once a refused
// connection closes.
//
// What it costs when this fails: a refused connection a program cannot shut
// stays open, unseen, holding a stranger's request at the socket.
func TestListenerSaysWhenARefusedConnectionWillNotClose(t *testing.T) {
	t.Parallel()
	var refused []refusal
	tell := func(principal string, known bool) { refused = append(refused, refusal{principal, known}) }
	c, err := Listener(&once{c: closeFails{}}, Principals(Unix(me())), tell).Accept()
	if c != nil || err == nil || errors.Is(err, net.ErrClosed) || len(refused) != 1 || refused[0] != (refusal{"", false}) {
		t.Errorf("a refused connection that will not close: %v, %v, refused told %v", c, err, refused)
	}
	c, err = Listener(&once{c: closes{}}, Principals(Unix(me())), nil).Accept()
	if c != nil || !errors.Is(err, net.ErrClosed) {
		t.Errorf("a refused connection that closes: %v, %v, want the listener's net.ErrClosed", c, err)
	}
}

// The gate starts no goroutine: Accept reads the peer, refuses and closes on
// the goroutine that called it, so the count of goroutines is the same while
// refused is told and after Accept returns as before it was called.
//
// What it costs when this fails: every connection a stranger opens leaves a
// goroutine behind, and a loop of connections exhausts the program.
//
//nolint:paralleltest // serial: it counts the process's goroutines, which a parallel test would change
func TestTheGateAddsNoGoroutine(t *testing.T) {
	s := unixPeer(t)
	before := runtime.NumGoroutine()
	during := -1
	g := Listener(&once{c: s}, Principals(Unix(me()+1)), func(string, bool) { during = runtime.NumGoroutine() })
	if c, err := g.Accept(); c != nil || !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept = %v, %v, want the refusal and then the listener's net.ErrClosed", c, err)
	}
	if after := runtime.NumGoroutine(); during != before || after != before {
		t.Errorf("goroutines: %d before Accept, %d while refused was told, %d after, want no change", before, during, after)
	}
}
