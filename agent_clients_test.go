//go:build windows || darwin

package gosocket_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agentLine is the line ssh-add -L prints for the test agent's key.
func agentLine() string {
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(agentKey()) + " " + agentComment
}

// agentClient is an OpenSSH client this system ships or a user installs, by
// the folder its ssh and ssh-add are in, and whether its ssh-add -L lists the
// key of an agent served through listen.Local and Listen.
type agentClient struct {
	name, dir string
	// lists is true where the client lists the agent's key, exiting 0. False,
	// the client cannot open such an agent: its ssh-add exits other than 0
	// and lists no key, and the README says what a program does in its place.
	lists bool
}

// An OpenSSH client run with SSH_AUTH_SOCK at an agent served through
// listen.Local and Listen opens it as the client's own agent: its ssh-add -L
// lists the agent's one key. Each client this system's programs meet has a
// row, and none is skipped: a client that cannot open such an agent has a
// row that wants its refusal. The rows run one after another, in their
// order, each against an agent of its own, so no client meets an instance
// another still holds; each logs its ssh -V and what its ssh-add said.
//
// What it costs when this fails: a program serves its agent where the ssh it
// runs, git's among them, does not look, and every fetch over SSH is refused
// for want of a key; or a client listed as refused starts listing the key,
// and the README sends programs past a client that works.
func TestOpenSSHClientsListTheKeyOfAnAgentServedAtListenLocal(t *testing.T) {
	t.Parallel()
	for _, cl := range agentClients(t) {
		listAgent(t, cl)
	}
}

// listAgent runs cl's ssh-add -L against an agent of its own, and reports
// where what it did is not what cl's row wants.
func listAgent(t *testing.T, cl agentClient) {
	t.Helper()
	addr := serveAgent(t)
	ctx, stop := context.WithTimeout(t.Context(), time.Minute)
	defer stop()
	version, err := exec.CommandContext(ctx, filepath.Join(cl.dir, "ssh"), "-V").CombinedOutput() //nolint:gosec // G204: the client's own ssh, in the folder its row names, with a constant argument
	t.Logf("%s: ssh -V: %s (%v)", cl.name, bytes.TrimSpace(version), err)
	cmd := exec.CommandContext(ctx, filepath.Join(cl.dir, "ssh-add"), "-L") //nolint:gosec // G204: the client's own ssh-add, in the folder its row names, with a constant argument
	cmd.Env = append(cmd.Environ(), "SSH_AUTH_SOCK="+addr)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	code := 0
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Errorf("%s's ssh-add in %s did not run: %v", cl.name, cl.dir, err)
		return
	}
	got := strings.TrimSpace(stdout.String())
	t.Logf("%s: ssh-add -L exited %d and said %q", cl.name, code, strings.TrimSpace(stderr.String()))
	switch {
	case cl.lists && (code != 0 || got != agentLine()):
		t.Errorf("%s's ssh-add -L with SSH_AUTH_SOCK=%s exited %d and listed %q, and want the agent's key %q",
			cl.name, addr, code, got, agentLine())
	case !cl.lists && (code == 0 || strings.Contains(got, agentLine())):
		t.Errorf("%s's ssh-add -L with SSH_AUTH_SOCK=%s exited %d and listed %q, and want it refused: an exit other than 0 and no key",
			cl.name, addr, code, got)
	}
}
