package listen

import (
	"bufio"
	"cmp"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// soon is a ctx for Listen, whose spans are the caller's: it ends with the
// test, or in ten seconds.
func soon(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// spanned is o with the three spans Serve requires that o leaves at zero,
// each long enough not to matter to a test of something else.
func spanned(o Options) Options {
	o.Grace = cmp.Or(o.Grace, 5*time.Second)
	o.ReadHeaderTimeout = cmp.Or(o.ReadHeaderTimeout, 10*time.Second)
	o.KeepAlive = cmp.Or(o.KeepAlive, time.Minute)
	return o
}

func tcp(t *testing.T) *Listener {
	t.Helper()
	l, err := Listen(soon(t), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func ask(t *testing.T, url string) string {
	t.Helper()
	r, err := getVia(t, http.DefaultClient, url)
	if err != nil {
		return err.Error()
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

// The server answers while asked, stays while a request is still being
// answered, and is gone once idle has passed after the last.
//
// What it costs when this fails: the program stays resident between visits,
// or quits under a slow request and the page loses half its data.
func TestTheServerIsGoneAfterTheLastRequest(t *testing.T) {
	t.Parallel()
	l := tcp(t)
	slow := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-slow
		}
		_, _ = io.WriteString(w, "ok")
	})
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- Serve(context.Background(), []*Listener{l}, Same(h), spanned(Options{Idle: NewIdle(200*time.Millisecond, time.Second)}))
	}()
	url := "http://" + l.Addr().String()
	if got := ask(t, url+"/"); got != "ok" {
		t.Fatalf("asked: %q", got)
	}
	got := make(chan string)
	go func() { got <- ask(t, url+"/slow") }()
	select {
	case err := <-done:
		t.Fatalf("stopped with a request in flight: %v", err)
	case <-time.After(600 * time.Millisecond):
	}
	close(slow)
	if s := <-got; s != "ok" {
		t.Errorf("the slow request got %q", s)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stopped with %v", err)
		}
		if took := time.Since(start); took < 800*time.Millisecond {
			t.Errorf("stopped %v after starting, before idle had passed after the last request", took)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("still serving long after the last request")
	}
}

// A process started for nothing leaves too: the span starts with it.
//
// What it costs when this fails: a program socket activation started for nothing stays up forever,
// holding memory and its socket, since no request ever arrives to start its clock.
func TestAServerNobodyAsksLeaves(t *testing.T) {
	t.Parallel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(context.Background(), []*Listener{tcp(t)}, Same(http.NotFoundHandler()), spanned(Options{Idle: NewIdle(100*time.Millisecond, time.Second)}))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("still serving with nobody asking")
	}
}

// A Hold keeps the process past its span with no request in flight, and
// releasing it starts the span again.
//
// What it costs when this fails: work a request started outside it is cut
// off when the process leaves, or the process never leaves.
func TestAHoldKeepsTheProcessUntilReleased(t *testing.T) {
	t.Parallel()
	idle := NewIdle(100*time.Millisecond, time.Second)
	release := idle.Hold()
	done := make(chan error, 1)
	go func() {
		done <- Serve(context.Background(), []*Listener{tcp(t)}, Same(http.NotFoundHandler()), spanned(Options{Idle: idle}))
	}()
	select {
	case err := <-done:
		t.Fatalf("left while held: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	released := time.Now()
	release()
	release() // twice is once
	select {
	case <-done:
		if took := time.Since(released); took < 100*time.Millisecond {
			t.Errorf("left %v after the release, before its span", took)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("still serving after the hold was released")
	}
}

// The end of ctx (a stop or a restart, by the caller's signal) lets the
// request in flight finish, and then Serve returns.
//
// What it costs when this fails: a restart cuts a request off halfway, and
// the page loses that answer.
func TestAStopLetsTheRequestInFlightFinish(t *testing.T) {
	t.Parallel()
	l := tcp(t)
	inside, release := make(chan struct{}), make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(inside)
		<-release
		_, _ = io.WriteString(w, "answered")
	})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, []*Listener{l}, Same(h), spanned(Options{Idle: NewIdle(time.Hour, time.Second)}))
	}()
	got := make(chan string, 1)
	go func() { got <- ask(t, "http://"+l.Addr().String()+"/") }()
	<-inside
	stop()
	select {
	case err := <-done:
		t.Fatalf("stopped with a request in flight: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if s := <-got; s != "answered" {
		t.Errorf("the request in flight got %q", s)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("stopped with %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("still serving after the stop")
	}
}

// Each listener gets the handler for it, and knows whether it is local: a
// socket, or on Windows a pipe.
//
// What it costs when this fails: a request on one socket is answered by another socket's handler,
// or told it came over TCP, and is served what the other was meant to serve.
func TestEachListenerGetsItsOwnHandler(t *testing.T) {
	t.Parallel()
	addr := local(t)
	u, err := Listen(soon(t), addr)
	if err != nil {
		t.Fatal(err)
	}
	ls := []*Listener{tcp(t), u}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ls, func(l *Listener) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, strconv.FormatBool(l.Local()))
			})
		}, spanned(Options{}))
	}()
	viaLocal := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialLocal(ctx, addr)
	}}}
	r, err := getVia(t, viaLocal, "http://local/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if got := ask(t, "http://"+ls[0].Addr().String()+"/"); got != "false" || string(b) != "true" {
		t.Errorf("TCP said %q, the local listener %q", got, b)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// connKey is the key Options.ConnContext keeps the connection under in the
// tests, as a program's check of its peer would.
type connKey struct{}

// keepConn is an Options.ConnContext: the connection, in the context.
func keepConn(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

// connOf answers each request with its connection's own address, or 403
// when its context holds none.
func connOf(w http.ResponseWriter, r *http.Request) {
	c, ok := r.Context().Value(connKey{}).(net.Conn)
	if !ok {
		http.Error(w, "no connection in the request", http.StatusForbidden)
		return
	}
	v, _ := r.Context().Value(markKey{}).(string)
	_, _ = io.WriteString(w, c.LocalAddr().String()+" "+v)
}

// Serve gives each request what Options.ConnContext adds, on every listener:
// here, the request's own connection, which a check of the peer reads.
//
// What it costs when this fails: every program that checks its peer finds no
// caller on any request, and refuses everybody, or one listener's
// connection reaches another's requests.
func TestServeGivesEachRequestWhatOptionsConnContextAdds(t *testing.T) {
	t.Parallel()
	a, b := tcp(t), tcp(t)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, []*Listener{a, b}, Same(http.HandlerFunc(connOf)), spanned(Options{ConnContext: keepConn}))
	}()
	asked := 0
	for _, l := range []*Listener{a, b} {
		asked++
		if got := ask(t, "http://"+l.Addr().String()+"/"); got != l.Addr().String()+" " {
			t.Errorf("a request on %s: %q", l.Addr(), got)
		}
	}
	if asked == 0 {
		t.Fatal("no listener was asked")
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// markKey is the key the tests' own ConnContext keeps its value under.
type markKey struct{}

// A listener's own ConnContext runs after Options.ConnContext, on that
// listener's connections alone: it finds the connection already there, and
// its value and the connection are both in the request. One that returns a
// fresh context drops the connection, and the request is refused, never
// answered.
//
// What it costs when this fails: a program's check of its peer finds no
// caller on any request, or one listener's value reaches another's requests,
// or a ConnContext that forgets its ctx lets a request past the check.
func TestAListenersConnContextRunsAfterOptions(t *testing.T) {
	t.Parallel()
	mark := func(ctx context.Context, _ net.Conn) context.Context {
		if _, ok := ctx.Value(connKey{}).(net.Conn); !ok {
			return context.WithValue(ctx, markKey{}, "before")
		}
		return context.WithValue(ctx, markKey{}, "a")
	}
	fresh := func(context.Context, net.Conn) context.Context { return context.Background() }
	a, b, c := tcp(t), tcp(t), tcp(t)
	a.ConnContext, c.ConnContext = mark, fresh
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, []*Listener{a, b, c}, Same(http.HandlerFunc(connOf)), spanned(Options{ConnContext: keepConn}))
	}()
	if got, want := ask(t, "http://"+a.Addr().String()+"/"), a.Addr().String()+" a"; got != want {
		t.Errorf("its own value, after the connection: %q, want %q", got, want)
	}
	if got, want := ask(t, "http://"+b.Addr().String()+"/"), b.Addr().String()+" "; got != want {
		t.Errorf("no value from another listener: %q, want %q", got, want)
	}
	if got := ask(t, "http://"+c.Addr().String()+"/"); !strings.Contains(got, "no connection") {
		t.Errorf("a fresh context was answered: %q", got)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The server's ConnContext is each that is set, Options' first, and none
// when neither is, as net/http's default.
//
// What it costs when this fails: a program's own ConnContext, or the one Options adds, is dropped,
// and its handlers lose what each connection was meant to carry.
func TestConnContextChainsWhatIsSet(t *testing.T) {
	t.Parallel()
	add := func(v string) func(context.Context, net.Conn) context.Context {
		return func(ctx context.Context, _ net.Conn) context.Context {
			was, _ := ctx.Value(markKey{}).(string)
			return context.WithValue(ctx, markKey{}, was+v)
		}
	}
	if connContext(nil, nil) != nil {
		t.Error("neither set, and the server has a ConnContext")
	}
	ran := 0
	for _, tc := range []struct {
		every, own func(context.Context, net.Conn) context.Context
		want       string
	}{
		{add("e"), nil, "e"},
		{nil, add("o"), "o"},
		{add("e"), add("o"), "eo"},
	} {
		ran++
		got, _ := connContext(tc.every, tc.own)(context.Background(), nil).Value(markKey{}).(string)
		if got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
	if ran != 3 {
		t.Fatalf("ran %d cases", ran)
	}
}

// Serve refuses each span at zero or below by its name, before it serves
// anything: Grace, ReadHeaderTimeout and KeepAlive, and an Idle's after and
// linger; each alone, and all three of Options at once in one error. The
// listener is left open, the caller's to close.
//
// What it costs when this fails: a span the caller left out becomes no wait
// at all, or one of the library's own the program never chose.
func TestServeRefusesEachSpanAtZeroByName(t *testing.T) {
	t.Parallel()
	all := "Options.Grace, Options.ReadHeaderTimeout, Options.KeepAlive"
	for _, tc := range []struct {
		names string
		o     Options
	}{
		{"Options.Grace", Options{ReadHeaderTimeout: time.Hour, KeepAlive: time.Hour}},
		{"Options.Grace", Options{Grace: -time.Second, ReadHeaderTimeout: time.Hour, KeepAlive: time.Hour}},
		{"Options.ReadHeaderTimeout", Options{Grace: time.Hour, KeepAlive: time.Hour}},
		{"Options.KeepAlive", Options{Grace: time.Hour, ReadHeaderTimeout: time.Hour}},
		{"NewIdle's after", spanned(Options{Idle: NewIdle(0, time.Hour)})},
		{"NewIdle's linger", spanned(Options{Idle: NewIdle(time.Hour, 0)})},
		{all, Options{}},
	} {
		l := tcp(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := Serve(ctx, []*Listener{l}, Same(http.NotFoundHandler()), tc.o)
		cancel()
		for name := range strings.SplitSeq(tc.names, ", ") {
			if err == nil || !strings.Contains(err.Error(), name+" is ") {
				t.Errorf("Serve with %s at zero or below: %v, and want it refused by that name", name, err)
			}
		}
		if err := l.Close(); err != nil {
			t.Errorf("the listener after Serve refused %s: %v, and want it left open", tc.names, err)
		}
	}
}

// serving is Serve on l with o, in the background, until ctx ends: what it
// returned comes on the channel.
func serving(ctx context.Context, l *Listener, h http.Handler, o Options) <-chan error {
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, []*Listener{l}, Same(h), o) }()
	return done
}

// closedAfter is how long c stayed open from now, reading until the server
// closed it; t fails if it is still open after ten seconds.
func closedAfter(t *testing.T, c net.Conn) time.Duration {
	t.Helper()
	start := time.Now()
	if err := c.SetReadDeadline(start.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, c); err != nil {
		t.Fatalf("the connection was not closed by the server: %v", err)
	}
	return time.Since(start)
}

// Each span Serve waits is the one the caller gave, none the library's own:
// a connection that sends half its headers is closed after ReadHeaderTimeout
// (the library's was 10 s), one with no request in it after KeepAlive (2
// minutes), a request in flight when ctx ends is cut off after Grace (5 s),
// and when an Idle is over, a connection that never sent a request is closed
// after its linger, and no sooner (1 s).
//
// What it costs when this fails: a program's server waits a span the program
// never chose, and its own, given, is not the one that holds.
func TestEachSpanServeWaitsIsTheCallers(t *testing.T) {
	t.Parallel()
	short := 200 * time.Millisecond
	t.Run("ReadHeaderTimeout", func(t *testing.T) {
		t.Parallel()
		l := tcp(t)
		done := serving(t.Context(), l, http.NotFoundHandler(), Options{Grace: time.Second, ReadHeaderTimeout: short, KeepAlive: time.Hour})
		c, err := dial(t, "tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n"); err != nil {
			t.Fatal(err)
		}
		if d := closedAfter(t, c); d > 5*time.Second {
			t.Errorf("half a request's headers held the connection %v, and want ReadHeaderTimeout's %v", d, short)
		}
		t.Cleanup(func() { <-done })
	})
	t.Run("KeepAlive", func(t *testing.T) {
		t.Parallel()
		l := tcp(t)
		done := serving(t.Context(), l, http.NotFoundHandler(), Options{Grace: time.Second, ReadHeaderTimeout: time.Hour, KeepAlive: short})
		c, err := dial(t, "tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		r, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		if d := closedAfter(t, c); d > 5*time.Second {
			t.Errorf("a connection with no request in it stayed %v, and want KeepAlive's %v", d, short)
		}
		t.Cleanup(func() { <-done })
	})
	t.Run("Grace", func(t *testing.T) {
		t.Parallel()
		l := tcp(t)
		inside, release := make(chan struct{}), make(chan struct{})
		defer close(release)
		h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(inside)
			<-release
		})
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		done := serving(ctx, l, h, Options{Grace: short, ReadHeaderTimeout: time.Hour, KeepAlive: time.Hour})
		go func() {
			if r, err := getVia(t, http.DefaultClient, "http://"+l.Addr().String()+"/"); err == nil {
				_ = r.Body.Close()
			}
		}()
		<-inside
		start := time.Now()
		stop()
		select {
		case err := <-done:
			if d := time.Since(start); d < short {
				t.Errorf("Serve returned %v after the stop, before Grace's %v", d, short)
			}
			if err != nil {
				t.Error(err)
			}
		case <-time.After(4 * time.Second):
			t.Errorf("Serve still waits on the request in flight 4 s after the stop, and want Grace's %v", short)
		}
	})
	t.Run("linger", func(t *testing.T) {
		t.Parallel()
		linger := 1500 * time.Millisecond
		l := tcp(t)
		start := time.Now()
		done := serving(t.Context(), l, http.NotFoundHandler(), spanned(Options{Idle: NewIdle(short, linger)}))
		c, err := dial(t, "tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		select {
		case err := <-done:
			if d := time.Since(start); d < linger {
				t.Errorf("Serve returned %v after it started, with a connection that sent no request open, and want it to wait the linger, %v, after the Idle's %v", d, linger, short)
			}
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve still serves 5 s after it started, with an Idle of 200 ms")
		}
	})
}

// A request on a listener of the program's own, a socket or on Windows a
// pipe, has what Options.ConnContext adds, as one on a TCP port has: its
// connection, which a check of the peer reads, whose local address is the
// path or the pipe's name.
//
// What it costs when this fails: a program that checks the peer of its socket, the one its gate
// reaches, refuses everybody.
func TestARequestOnALocalListenerHasWhatOptionsConnContextAdds(t *testing.T) {
	t.Parallel()
	addr := local(t)
	u, err := Listen(soon(t), addr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, []*Listener{u}, Same(http.HandlerFunc(connOf)), spanned(Options{ConnContext: keepConn}))
	}()
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialLocal(ctx, addr)
	}}}
	r, err := getVia(t, c, "http://local/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if got := string(body); r.StatusCode != http.StatusOK || got != addr+" " {
		t.Errorf("a request on the local listener: %d %q, and want its local address %s", r.StatusCode, got, addr)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
