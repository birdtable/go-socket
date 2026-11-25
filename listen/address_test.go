package listen

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// An address is local, a socket's path or a pipe's name, when it starts /, . or \, or is
// absolute on the system (a drive's on Windows), and a TCP address otherwise; a TCP listener is
// named by the address given and is not local. Which local names a system takes is
// listenLocal's to say, and each refuses the other's with ErrPipeName.
//
// What it costs when this fails: a pipe's name or a Windows path is listened on as a TCP
// address and refused strangely, or a TCP address is taken for a socket's path and a file is
// made by its name.
func TestAnAddressIsLocalOrATCPAddress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr  string
		local bool
	}{
		{"/run/app.sock", true},
		{"./app.sock", true},
		{"../app.sock", true},
		{`\\.\pipe\app`, true},
		{`\app.sock`, true},
		{"127.0.0.1:0", false},
		{"localhost:8080", false},
		{":8080", false},
		{`C:\Users\app.sock`, runtime.GOOS == "windows"},
	}
	ran := 0
	for _, tc := range cases {
		ran++
		if got := isLocal(tc.addr); got != tc.local {
			t.Errorf("%q is local: %v, and want %v", tc.addr, got, tc.local)
		}
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
	l := tcp(t)
	defer l.Close()
	if l.Name() != "127.0.0.1:0" || l.Local() {
		t.Errorf("a TCP listener is named %q, local %v", l.Name(), l.Local())
	}
}

// A ctx with no deadline is refused for a local listener as for a TCP one, naming the deadline,
// before anything is made.
//
// What it costs when this fails: Listen waits a span of its own the program never chose.
func TestALocalListenWithNoDeadlineIsRefused(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{local(t), "127.0.0.1:0"} {
		l, err := Listen(context.Background(), addr)
		if err == nil {
			_ = l.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "no deadline") {
			t.Errorf("Listen(%s) with a ctx with no deadline: %v, and want it refused, naming the deadline", addr, err)
		}
	}
}
