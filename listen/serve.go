package listen

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Options say when Serve returns, and how long each wait it makes lasts.
// Every span is the caller's: Serve has no number of its own, and refuses a
// span at zero or below, by its name, before it serves anything.
type Options struct {
	// Idle, when set, ends Serve once its span has passed with nothing held:
	// systemd starts the program again on the next request. Nil serves until
	// ctx ends, where nothing would start it again (a container).
	Idle *Idle
	// Grace is how long the requests in flight get when ctx ends (a stop or
	// a restart). What is still open after is closed. Required.
	Grace time.Duration
	// ReadHeaderTimeout is how long a connection may take to send a
	// request's headers, as http.Server's: a caller that sends them slowly
	// holds a connection no longer. Required.
	ReadHeaderTimeout time.Duration
	// KeepAlive is how long a connection with no request in it stays open
	// between requests, as http.Server's IdleTimeout. Required.
	KeepAlive time.Duration
	// ConnContext, when set, adds to each connection's context on every
	// listener, before the listener's own: what a program's check of its peer
	// reads, such as the connection itself. It must build on the ctx it is
	// given, and must not return nil, as Listener.ConnContext says.
	ConnContext func(ctx context.Context, c net.Conn) context.Context
}

// spans is every span o must give, each over zero, by its name: nil when
// each is.
func (o Options) spans() error {
	var errs []error
	for _, s := range []struct {
		name string
		d    time.Duration
	}{
		{"Options.Grace", o.Grace},
		{"Options.ReadHeaderTimeout", o.ReadHeaderTimeout},
		{"Options.KeepAlive", o.KeepAlive},
	} {
		if s.d <= 0 {
			errs = append(errs, fmt.Errorf("listen.Serve: %s is %v, and must be over zero: the span is the caller's", s.name, s.d))
		}
	}
	if o.Idle != nil {
		if o.Idle.after <= 0 {
			errs = append(errs, fmt.Errorf("listen.Serve: NewIdle's after is %v, and must be over zero: the span is the caller's", o.Idle.after))
		}
		if o.Idle.linger <= 0 {
			errs = append(errs, fmt.Errorf("listen.Serve: NewIdle's linger is %v, and must be over zero: the span is the caller's", o.Idle.linger))
		}
	}
	return errors.Join(errs...)
}

// Serve answers on every listener, each with handler's answer for it, until
// ctx ends or o.Idle is over. An error is a listener failing, not a request,
// or a span of o's at zero or below: then Serve returns before it serves
// anything, and the listeners are left open, the caller's to close.
// Each request's context carries what o.ConnContext adds, and then what the
// listener's own ConnContext adds, on that listener's connections only.
func Serve(ctx context.Context, ls []*Listener, handler func(*Listener) http.Handler, o Options) error {
	if err := o.spans(); err != nil {
		return err
	}
	var quiet <-chan struct{}
	if o.Idle != nil {
		quiet = o.Idle.quiet
	}
	servers := make([]*http.Server, 0, len(ls))
	failed := make(chan error, len(ls))
	for _, l := range ls {
		h := handler(l)
		if o.Idle != nil {
			h = o.Idle.Handler(h)
		}
		srv := &http.Server{Handler: h, ReadHeaderTimeout: o.ReadHeaderTimeout, IdleTimeout: o.KeepAlive, ConnContext: connContext(o.ConnContext, l.ConnContext)}
		servers = append(servers, srv)
		go func() { failed <- srv.Serve(l) }()
	}
	stop := func(grace time.Duration) {
		sctx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		for _, srv := range servers {
			if srv.Shutdown(sctx) != nil {
				_ = srv.Close() //nolint:errcheck // closing what the grace did not see finish
			}
		}
	}
	for {
		select {
		case err := <-failed:
			if !errors.Is(err, http.ErrServerClosed) {
				stop(0)
				return err
			}
		case <-ctx.Done():
			stop(o.Grace)
			return nil
		case <-quiet:
			if o.Idle.Over() {
				// For the Idle's linger, kept short: a browser keeps spare
				// connections open that never send a request, and Shutdown
				// would wait for them. None carries one now, and a browser
				// that comes back connects again.
				stop(o.Idle.linger)
				return nil
			}
		}
	}
}

// connContext is every's, then the listener's own, each when set: nil when
// neither is.
func connContext(every, own func(context.Context, net.Conn) context.Context) func(context.Context, net.Conn) context.Context {
	switch {
	case every == nil:
		return own
	case own == nil:
		return every
	}
	return func(ctx context.Context, c net.Conn) context.Context {
		return own(every(ctx, c), c)
	}
}

// Same is handler for a program that answers every listener alike.
func Same(h http.Handler) func(*Listener) http.Handler {
	return func(*Listener) http.Handler { return h }
}
