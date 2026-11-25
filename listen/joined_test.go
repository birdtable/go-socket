package listen

import (
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
)

// failingClose is a listener whose Close says err, and counts the calls.
type failingClose struct {
	net.Listener
	err    error
	closed int
}

func (f *failingClose) Close() error {
	f.closed++
	return f.err
}

// TestJoinedKeepsTheStoppingErrorAndSaysAFailingCloseAfterIt: a clean close gives back the
// very error it was given, so what a caller compares or matches is untouched; a failing close
// leaves the stopping error first and matching, and adds its own text and its own match. The
// plants are joined with the close dropped, with the stopping error dropped, and with a clean
// close joined anyway, each red here.
//
// What it costs when this fails: a listener that answers already is reported as the bare
// "use of closed network connection" of the close that came after it, or a caller's == on the
// error stops being true.
func TestJoinedKeepsTheStoppingErrorAndSaysAFailingCloseAfterIt(t *testing.T) {
	t.Parallel()
	stop := errors.New("box.sock answers already: something else listens there")
	closing := errors.New("use of closed network connection")

	// The very error is the one value, not one that matches: a join of it has another type.
	if got := joined(stop, nil); reflect.TypeOf(got) != reflect.TypeOf(stop) || got.Error() != stop.Error() {
		t.Errorf("a clean close changed the error: got %#v, want the very error %#v", got, stop)
	}

	got := joined(stop, closing)
	if !errors.Is(got, stop) || !errors.Is(got, closing) {
		t.Errorf("joined(stop, closing) = %q, want one that matches both errors", got)
	}
	if text := got.Error(); !strings.HasPrefix(text, stop.Error()) || !strings.Contains(text, closing.Error()) {
		t.Errorf("joined(stop, closing) says %q, want the stopping error first and the close's after it", text)
	}

	if got := joined(nil, closing); !errors.Is(got, closing) || got.Error() != closing.Error() {
		t.Errorf("joined(nil, closing) = %v, want the close's error alone", got)
	}
	if got := joined(nil, nil); got != nil {
		t.Errorf("joined(nil, nil) = %v, want nil", got)
	}
}

// TestCloseAllClosesEachListenerAndSaysEveryFailure: every listener is closed even when one
// fails, and the error says each failure. The plant is a closeAll that stops at the first, or
// keeps one.
//
// What it costs when this fails: a listener left open after a failed activation, or a failure
// said of one and not of the rest.
func TestCloseAllClosesEachListenerAndSaysEveryFailure(t *testing.T) {
	t.Parallel()
	first, second := errors.New("first close failed"), errors.New("second close failed")
	a, b, c := &failingClose{err: first}, &failingClose{}, &failingClose{err: second}
	err := closeAll([]*Listener{{Listener: a}, {Listener: b}, {Listener: c}})
	if a.closed != 1 || b.closed != 1 || c.closed != 1 {
		t.Errorf("closes were %d, %d, %d, want each listener closed once", a.closed, b.closed, c.closed)
	}
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Errorf("closeAll = %v, want one that matches both failures", err)
	}
	if err := closeAll([]*Listener{{Listener: &failingClose{}}}); err != nil {
		t.Errorf("closeAll of a clean close = %v, want nil", err)
	}
}
