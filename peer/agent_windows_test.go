package peer

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// DialAgent dials the pipe SSH_AUTH_SOCK names by DialPipe, so the agent
// reads this account as its caller. The level it dials at is DialPipe's,
// identification; no test here reads it, as reading it takes a raw call the
// security register does not hold.
//
// What it costs when this fails: an agent at the pipe the variable names
// reads nobody, or another account, and refuses every request.
func TestDialAgentDialsThePipeSSHAuthSockNamesAsThisAccount(t *testing.T) {
	t.Parallel()
	me := self(t)
	l, name := listen(t, me)
	got := acceptOne(l)
	c, err := DialAgent(t.Context(), agentAt(name))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a := <-got
	if a.err != nil {
		t.Fatal(a.err)
	}
	defer a.c.Close()
	if p, err := PeerOf(a.c); err != nil || p.User != me {
		t.Errorf("the agent's caller is %+v, %v, and want %s", p, err, me)
	}
}

// A pipe this account serves at a fresh name is taken for the agent's: its
// owner, which PeerOf names on the dialling side, is this account, and the
// owner check after the dial lets it through.
//
// What it costs when this fails: an agent the user runs as themselves, at a
// pipe SSH_AUTH_SOCK names, is refused as another account's.
func TestDialAgentTakesAPipeThisAccountServes(t *testing.T) {
	t.Parallel()
	me := self(t)
	l, name := listen(t, me)
	got := acceptOne(l)
	c, err := DialAgent(t.Context(), agentAt(name))
	if err != nil {
		t.Fatalf("a pipe this account serves: %v, and want it taken", err)
	}
	defer c.Close()
	a := <-got
	if a.err != nil {
		t.Fatal(a.err)
	}
	defer a.c.Close()
	if p, err := PeerOf(c); err != nil || p.User != me {
		t.Errorf("the pipe's owner is %+v, %v, and want %s", p, err, me)
	}
}

// A pipe whose owner is neither the process's own user, SYSTEM nor
// BUILTIN\Administrators is closed and refused after the dial, naming its
// owner. The pipe here is this account's, dialled as if the process were
// another account, stranger, so the check that runs after DialPipe goes red
// on a machine with one account. A pipe a second account made is owed to a
// run on a machine that has one.
//
// What it costs when this fails: an account that made a pipe at the agent's
// name, while the agent is not up, reads the product's keys and requests and
// answers them with its own.
func TestDialAgentRefusesAPipeAnotherAccountOwns(t *testing.T) {
	t.Parallel()
	me := self(t)
	l, name := listen(t, me)
	got := acceptOne(l)
	// The client is closed as it is refused, so go-winio's Accept may wait on
	// for the next: its answer is taken whenever it comes, at the latest when
	// the listener closes as the test ends.
	go func() {
		if a := <-got; a.err == nil {
			_ = a.c.Close()
		}
	}()
	c, err := dialAgentAs(t.Context(), name, stranger)
	if !errors.Is(err, errAgentOwner) || !errors.Is(err, fs.ErrPermission) || c != nil {
		t.Fatalf("a pipe %s owns, dialled as %s: %v, %v, and want it refused with fs.ErrPermission", me, stranger, c, err)
	}
	if !strings.Contains(err.Error(), me) {
		t.Errorf("the refusal %q does not name the pipe's owner %s", err, me)
	}
}

// A ctx with no deadline is bounded by DialAgent itself: a pipe whose name is
// held and never served answers busy, and the dial ends with
// context.DeadlineExceeded, not fs.ErrNotExist. The test gives the bound as
// 100 ms, not the product's 5 s, which TestDialAgentsBoundIsFiveSeconds
// checks. The bound's length is not asserted; with no bound the dial never
// ends, and the package's timeout fails the run.
//
// What it costs when this fails: any account that holds the agent's name
// and never serves it keeps a product that asks for its agent waiting for
// ever.
func TestDialAgentWithNoDeadlineEndsOnAPipeNeverServed(t *testing.T) {
	t.Parallel()
	_, name := listen(t, self(t))
	ctx := t.Context()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("the test's context has a deadline, so DialAgent's own bound is not what ends the dial")
	}
	c, err := dialAgentWithin(ctx, agentAt(name), 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, fs.ErrNotExist) || c != nil {
		t.Errorf("a pipe held and never served, with no deadline: %v, %v, and want context.DeadlineExceeded", c, err)
	}
}

// An SSH_AUTH_SOCK that is not a pipe on this machine is refused as DialPipe
// refuses its name, and nothing is dialled: a path, a Unix socket's among
// them, another machine's pipe, a name with a separator, and the forward
// slashes Win32 reads as \.
//
// What it costs when this fails: a program hands its identity to a pipe on
// another machine, or dials a file for an agent.
func TestAnSSHAuthSockThatIsNotAPipeHereIsRefused(t *testing.T) {
	t.Parallel()
	for _, v := range []string{`C:\Users\me\agent.sock`, `/tmp/ssh-x/agent.1`, `\\server\pipe\openssh-ssh-agent`,
		`\\.\pipe\a\b`, `//./pipe/openssh-ssh-agent`, `\\.\pipe\openssh-ssh-agent.`} {
		if c, err := DialAgent(t.Context(), agentAt(v)); !errors.Is(err, errPipeName) || c != nil {
			t.Errorf("SSH_AUTH_SOCK=%q: %v, %v, and want the pipe's name refused", v, c, err)
		}
	}
}

// A pipe nobody listens on is no agent: fs.ErrNotExist.
//
// What it costs when this fails: a program cannot tell "no agent here", for
// which it asks for a password or a key file, from a fault.
func TestNoAgentAtThePipeIsNotExist(t *testing.T) {
	t.Parallel()
	if c, err := DialAgent(t.Context(), agentAt(pipeName())); !errors.Is(err, fs.ErrNotExist) || c != nil {
		t.Errorf("a pipe nobody listens on: %v, %v, and want fs.ErrNotExist", c, err)
	}
}

// With SSH_AUTH_SOCK unset DialAgent dials Windows' OpenSSH agent service's
// pipe. Where the service runs, its agent answers a request for its
// identities with its list, so it takes the caller DialPipe names; where it
// does not, there is no agent: fs.ErrNotExist, naming that pipe. Each case
// is asserted, and neither is skipped.
//
// What it costs when this fails: a program on Windows finds no agent where
// the user runs one, the agent refuses its caller, or a stopped service reads
// as a fault.
func TestWithSSHAuthSockUnsetOpenSSHsAgentIsDialled(t *testing.T) {
	t.Parallel()
	c, err := DialAgent(t.Context(), agentAt(""))
	if errors.Is(err, fs.ErrNotExist) {
		if c != nil || !strings.Contains(err.Error(), openSSHAgentPipe) {
			t.Errorf("no OpenSSH agent: %v, %v, and want fs.ErrNotExist naming %s", c, err, openSSHAgentPipe)
		}
		return
	}
	if err != nil {
		t.Fatalf("OpenSSH's agent pipe: %v, and want the agent or fs.ErrNotExist", err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// SSH_AGENTC_REQUEST_IDENTITIES (11), framed by its length; the answer is
	// SSH_AGENT_IDENTITIES_ANSWER (12).
	if _, err := c.Write([]byte{0, 0, 0, 1, 11}); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 5)
	if _, err := io.ReadFull(c, head); err != nil {
		t.Fatalf("the agent did not answer: %v", err)
	}
	if head[4] != 12 {
		t.Errorf("the agent answered with message %d, and want 12, its identities: 5 is its refusal", head[4])
	}
}
