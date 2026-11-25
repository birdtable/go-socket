//go:build !windows

package peer

import (
	"context"
	"fmt"
	"net"
)

// errNoPipes is ListenPipe and DialPipe off Windows, which alone has the named
// pipes they make.
var errNoPipes = fmt.Errorf("a named pipe is Windows' alone: %w", ErrCannotSay)

// ListenPipe is ErrCannotSay here: a named pipe is Windows' alone.
func ListenPipe(name string, sids ...string) (net.Listener, error) { return nil, errNoPipes }

// DialPipe is ErrCannotSay here: a named pipe is Windows' alone.
func DialPipe(ctx context.Context, name string) (net.Conn, error) { return nil, errNoPipes }
