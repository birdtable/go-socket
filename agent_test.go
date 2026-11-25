package gosocket_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/birdtable/go-socket/listen"
	"github.com/birdtable/go-socket/peer"
)

// The SSH agent protocol's messages the test agent knows
// (draft-miller-ssh-agent, section 6.1): it answers a request for its
// identities, and fails every other request.
const (
	agentFailure           = 5
	agentRequestIdentities = 11
	agentIdentitiesAnswer  = 12
	// agentComment is the comment on the test agent's one key.
	agentComment = "go-socket-test-agent"
)

// agentKey is the test agent's one key, an ed25519 key from a fixed seed, as
// the agent protocol sends it: a blob, the key type and the key, each a
// string.
func agentKey() []byte {
	pub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	var b []byte
	b = sshString(b, []byte("ssh-ed25519"))
	return sshString(b, pub)
}

// sshString appends s to b as the SSH wire's string: its length, four bytes
// big-endian, then its bytes.
func sshString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

// serveAgent serves the test agent on a listener listen.Listen makes at
// listen.Local's address for "agent" in a folder of the test's, the address
// an SSH_AUTH_SOCK holds, and returns it. The listener is closed when the
// test ends.
func serveAgent(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	addr, err := listen.Local(dir, "agent")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(t.Context(), time.Minute)
	defer stop()
	l, err := listen.Listen(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
		<-served
	})
	go func() {
		defer close(served)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go answerAgent(c)
		}
	}()
	return addr
}

// answerAgent answers each request on c until the client closes it: its one
// key for a request for identities, a failure for any other.
func answerAgent(c net.Conn) {
	defer c.Close()
	for {
		var n uint32
		if err := binary.Read(c, binary.BigEndian, &n); err != nil || n == 0 || n > 1<<16 {
			return
		}
		msg := make([]byte, n)
		if _, err := io.ReadFull(c, msg); err != nil {
			return
		}
		answer := []byte{agentFailure}
		if msg[0] == agentRequestIdentities {
			answer = binary.BigEndian.AppendUint32([]byte{agentIdentitiesAnswer}, 1)
			answer = sshString(answer, agentKey())
			answer = sshString(answer, []byte(agentComment))
		}
		if _, err := c.Write(sshString(nil, answer)); err != nil {
			return
		}
	}
}

// identities asks the agent on c for its identities and returns the first
// key's blob and comment.
func identities(c net.Conn) (blob []byte, comment string, err error) {
	if _, err := c.Write(sshString(nil, []byte{agentRequestIdentities})); err != nil {
		return nil, "", err
	}
	var n uint32
	if err := binary.Read(c, binary.BigEndian, &n); err != nil {
		return nil, "", err
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(c, msg); err != nil {
		return nil, "", err
	}
	field := func() ([]byte, error) {
		if len(msg) < 4 || uint32(len(msg)-4) < binary.BigEndian.Uint32(msg) {
			return nil, errors.New("the answer ends inside a field")
		}
		f := msg[4 : 4+binary.BigEndian.Uint32(msg)]
		msg = msg[4+len(f):]
		return f, nil
	}
	if len(msg) < 5 || msg[0] != agentIdentitiesAnswer || binary.BigEndian.Uint32(msg[1:]) != 1 {
		return nil, "", fmt.Errorf("the agent answered %x, and want one identity", msg)
	}
	msg = msg[5:]
	if blob, err = field(); err != nil {
		return nil, "", err
	}
	c2, err := field()
	return blob, string(c2), err
}

// An SSH agent served through listen.Local and Listen, as a test of a
// program's serves one, is the agent peer.DialAgent dials when
// SSH_AUTH_SOCK holds that address: its one key comes back with its comment.
// On Windows the address is a pipe, and DialAgent dials it by DialPipe.
//
// What it costs when this fails: a program's agent, or a test's, is served
// where DialAgent does not look, and the program finds no key.
func TestDialAgentReachesAnAgentServedAtListenLocal(t *testing.T) {
	t.Parallel()
	addr := serveAgent(t)
	ctx, stop := context.WithTimeout(t.Context(), time.Minute)
	defer stop()
	c, err := peer.DialAgent(ctx, func(k string) string {
		if k == "SSH_AUTH_SOCK" {
			return addr
		}
		return ""
	})
	if err != nil {
		t.Fatalf("DialAgent with SSH_AUTH_SOCK=%s: %v", addr, err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	blob, comment, err := identities(c)
	if err != nil || !bytes.Equal(blob, agentKey()) || comment != agentComment {
		t.Errorf("the agent at %s answered %x %q (%v), and want its key %x %q", addr, blob, comment, err, agentKey(), agentComment)
	}
}
