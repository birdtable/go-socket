//go:build !windows

package peer

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"path/filepath"
)

// dialAgent dials the agent's socket at addr, SSH_AUTH_SOCK's value: an
// absolute path, cleaned before it is checked and dialled. Unset is no agent;
// a relative path, which would be dialled from wherever the program runs, or
// on Linux "@" and a name, an abstract socket any account may bind first, is
// refused.
func dialAgent(ctx context.Context, addr string) (net.Conn, error) {
	if addr == "" {
		return nil, fmt.Errorf("%s is not set, so this session has no SSH agent: %w", agentEnv, fs.ErrNotExist)
	}
	p := filepath.Clean(addr)
	if !filepath.IsAbs(p) {
		return nil, fmt.Errorf("%s is %q, and an SSH agent's socket is an absolute path: %w", agentEnv, addr, fs.ErrInvalid)
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", p)
	if err != nil {
		return nil, fmt.Errorf("the SSH agent at %s (%s): %w", p, agentEnv, err)
	}
	return c, nil
}
