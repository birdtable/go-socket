//go:build unix

package peer

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// fakeAgent is a socket listening in a folder of its own under /tmp, as an
// agent's is, and the folder.
func fakeAgent(t *testing.T) (net.Listener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "peer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "unix", filepath.Join(dir, "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, dir
}

// DialAgent dials the socket SSH_AUTH_SOCK names, in its cleaned form, and
// the agent's end of the connection is this user's.
//
// What it costs when this fails: a program cannot reach the user's agent, or
// reaches another socket than the one named.
func TestDialAgentDialsTheSocketSSHAuthSockNames(t *testing.T) {
	t.Parallel()
	l, dir := fakeAgent(t)
	for _, v := range []string{filepath.Join(dir, "agent.sock"), dir + "/./agent.sock", dir + "//agent.sock"} {
		got := make(chan net.Conn, 1)
		go func() {
			s, err := l.Accept()
			if err != nil {
				s = nil
			}
			got <- s
		}()
		c, err := DialAgent(t.Context(), agentAt(v))
		if err != nil {
			t.Fatalf("SSH_AUTH_SOCK=%s: %v", v, err)
		}
		s := <-got
		if s == nil {
			t.Fatalf("SSH_AUTH_SOCK=%s: the agent took no connection", v)
		}
		if _, err := io.WriteString(c, "x"); err != nil {
			t.Error(err)
		}
		b := make([]byte, 1)
		if _, err := io.ReadFull(s, b); err != nil || b[0] != 'x' {
			t.Errorf("SSH_AUTH_SOCK=%s: the agent read %q, %v", v, b, err)
		}
		if p, err := PeerOf(s); err == nil {
			if uid, ok := p.UID(); !ok || uid != me() {
				t.Errorf("SSH_AUTH_SOCK=%s: the agent's caller is %+v, and want uid %d", v, p, me())
			}
		} else if !errors.Is(err, ErrCannotSay) {
			t.Error(err)
		}
		_ = c.Close()
		_ = s.Close()
	}
}

// No agent is fs.ErrNotExist: SSH_AUTH_SOCK unset or empty, or a path with
// no socket at it.
//
// What it costs when this fails: a program cannot tell "no agent here", for
// which it asks for a password or a key file, from a fault.
func TestNoAgentIsNotExist(t *testing.T) {
	t.Parallel()
	_, dir := fakeAgent(t)
	for _, v := range []string{"", filepath.Join(dir, "gone.sock")} {
		if c, err := DialAgent(t.Context(), agentAt(v)); !errors.Is(err, fs.ErrNotExist) || c != nil {
			t.Errorf("SSH_AUTH_SOCK=%q: %v, %v, and want fs.ErrNotExist", v, c, err)
		}
	}
}

// A relative SSH_AUTH_SOCK is refused before anything is dialled, even one
// naming a socket that listens from where the program runs; so is "@" and a
// name, which Linux would dial as an abstract socket, which any account may
// bind first.
//
// What it costs when this fails: a program run from a folder another account
// writes to hands its agent requests, and what it signs, to that account's
// socket.
func TestARelativeSSHAuthSockIsRefused(t *testing.T) {
	// serial: it changes the working folder with t.Chdir, which a parallel test may not.
	_, dir := fakeAgent(t)
	t.Chdir(dir)
	for _, v := range []string{"agent.sock", "./agent.sock", "@agent", "../" + filepath.Base(dir) + "/agent.sock"} {
		if c, err := DialAgent(t.Context(), agentAt(v)); !errors.Is(err, fs.ErrInvalid) || c != nil {
			t.Errorf("SSH_AUTH_SOCK=%q: %v, %v, and want fs.ErrInvalid", v, c, err)
		}
	}
}
