package listen

import (
	"net/http"
	"sync"
	"time"
)

// Idle is how long a process stays after the last thing it was doing, and
// what it is doing now: every request, and every Hold. Between requests
// nothing of it runs but one timer, set when the last one ends.
type Idle struct {
	after  time.Duration
	linger time.Duration
	mu     sync.Mutex
	held   int
	last   time.Time
	timer  *time.Timer
	quiet  chan struct{}
}

// NewIdle is a process that leaves after this long with nothing held. The
// span starts now, so a process started for nothing leaves too. linger is how
// long the connections still open then, with no request in them, get before
// they are closed. Both are the caller's: Serve refuses an Idle with either at
// zero or below, by its name.
func NewIdle(after, linger time.Duration) *Idle {
	i := &Idle{after: after, linger: linger, last: time.Now(), quiet: make(chan struct{}, 1)}
	i.timer = time.AfterFunc(after, func() {
		select {
		case i.quiet <- struct{}{}:
		default:
		}
	})
	return i
}

// Hold keeps the process until release is called: work that outlives the
// request that started it, or a connection kept open with no request in it.
// release is safe to call twice.
func (i *Idle) Hold() (release func()) {
	i.mu.Lock()
	i.held++
	i.timer.Stop()
	i.mu.Unlock()
	return sync.OnceFunc(func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		i.held--
		i.last = time.Now()
		if i.held == 0 {
			i.timer.Reset(i.after)
		}
	})
}

// Handler holds the process for each request h answers.
func (i *Idle) Handler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer i.Hold()()
		h.ServeHTTP(w, r)
	})
}

// Over is whether the span has passed with nothing held. The timer can fire
// just as a request ends and sets it again; the second firing is the one
// that counts.
func (i *Idle) Over() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.held == 0 && time.Since(i.last) >= i.after
}
