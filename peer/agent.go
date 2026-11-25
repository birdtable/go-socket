package peer

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"time"
)

// agentEnv is the variable that names the SSH agent's socket or pipe.
const agentEnv = "SSH_AUTH_SOCK"

// agentDialBound is how long DialAgent waits for an agent when ctx has no
// deadline of its own: a pipe's name held and never served answers busy, and
// a dial would wait on it for ever.
const agentDialBound = 5 * time.Second

// The two accounts besides this process's own user whose pipe DialAgent takes
// for the agent's: SYSTEM, and BUILTIN\Administrators, which Windows makes the
// owner of what an elevated or service token makes.
const (
	systemSID         = "S-1-5-18"
	administratorsSID = "S-1-5-32-544"
)

// errAgentOwner is an agent's pipe owned by an account that is neither this
// process's own user, SYSTEM nor BUILTIN\Administrators: any account may make
// a pipe at a name nobody serves, and would read every request.
var errAgentOwner = fmt.Errorf("an SSH agent's pipe is taken only when this account, SYSTEM (%s) or BUILTIN\\Administrators (%s) owns it, as any account may make a pipe at a name nobody serves and would read every request sent to the agent: %w",
	systemSID, administratorsSID, fs.ErrPermission)

// DialAgent is a connection to the SSH agent of this session, for
// golang.org/x/crypto/ssh/agent's NewClient. env reads the environment, as
// os.Getenv does, so a test can aim it at an agent of its own. It dials what
// SSH_AUTH_SOCK names: on Unix the path of the agent's socket, which must be
// absolute; on Windows a named pipe on this machine, \\.\pipe\ and then a
// name, dialled by DialPipe, at the identification level, so the agent can
// read who this account is and cannot act as it. Where SSH_AUTH_SOCK is
// unset or empty, on Windows it dials the pipe of Windows' OpenSSH agent
// service, \\.\pipe\openssh-ssh-agent, and on Unix there is no agent. No
// agent, unset or not there, wraps fs.ErrNotExist. A relative path, on
// Linux an abstract socket among them, a path on Windows that is not a pipe
// on this machine, and a nil env are refused, and nothing is dialled.
//
// On Windows the pipe dialled, named or OpenSSH's, is taken only when its
// owner is this process's own user, SYSTEM (S-1-5-18) or
// BUILTIN\Administrators (S-1-5-32-544): any account may make a pipe at a
// name nobody serves. A pipe any other account owns is closed and refused,
// wrapping fs.ErrPermission, with an error that names the owner's SID.
//
// A ctx with no deadline is given one of 5 seconds for the dial alone, as a
// pipe's name held and never served would keep it waiting for ever: an agent
// not there by then wraps context.DeadlineExceeded. A ctx with a deadline of
// its own keeps it.
func DialAgent(ctx context.Context, env func(string) string) (net.Conn, error) {
	return dialAgentWithin(ctx, env, agentDialBound)
}

// dialAgentWithin is DialAgent with bound in place of agentDialBound for a
// ctx with no deadline, so a test waits out a bound of its own and not the
// product's. It is a parameter, not a package value a test writes, so
// DialAgent's tests run in parallel with no race on it.
func dialAgentWithin(ctx context.Context, env func(string) string, bound time.Duration) (net.Conn, error) {
	if env == nil {
		return nil, fmt.Errorf("DialAgent reads %s through env, and env is nil: pass os.Getenv: %w", agentEnv, fs.ErrInvalid)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, bound)
		defer cancel()
	}
	return dialAgent(ctx, env(agentEnv))
}

// agentOwner says whether an agent's pipe whose owner is the SID owner may be
// taken by a process whose own user is the SID user: owner is user, SYSTEM or
// BUILTIN\Administrators. An empty owner is nobody's, and refused.
func agentOwner(owner, user string) error {
	if owner != "" && (owner == user || owner == systemSID || owner == administratorsSID) {
		return nil
	}
	return fmt.Errorf("the pipe is owned by %q: %w", owner, errAgentOwner)
}
