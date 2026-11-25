package peer

import (
	"context"
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/windows"
)

// openSSHAgentPipe is the pipe Windows' OpenSSH agent service listens on.
const openSSHAgentPipe = `\\.\pipe\openssh-ssh-agent`

// dialAgent dials the agent's pipe at addr, SSH_AUTH_SOCK's value, or
// OpenSSH's where it is empty, by DialPipe, and takes it only if its owner is
// this process's own user, the user of its token, SYSTEM or
// BUILTIN\Administrators (agentOwner).
func dialAgent(ctx context.Context, addr string) (net.Conn, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("this process's own account, which an SSH agent's pipe is checked against, could not be read: %w", err)
	}
	return dialAgentAs(ctx, addr, u.User.Sid.String())
}

// dialAgentAs is dialAgent for a process whose own user is the SID user: its
// name is checked as DialPipe checks every name, it dials at the
// identification level, and the pipe's owner, read from the handle opened,
// must be one agentOwner takes for user, or the pipe is closed and refused.
func dialAgentAs(ctx context.Context, addr, user string) (net.Conn, error) {
	from := agentEnv
	if addr == "" {
		addr, from = openSSHAgentPipe, "OpenSSH's, as "+agentEnv+" is not set"
	}
	c, err := DialPipe(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("the SSH agent at %s (%s): on Windows an agent is a named pipe on this machine: %w", addr, from, err)
	}
	p, err := peerOf(c)
	if err == nil {
		err = agentOwner(p.User, user)
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("the SSH agent at %s (%s): %w", addr, from, err), c.Close())
	}
	return c, nil
}
