package peer

import (
	"errors"
	"io/fs"
	"testing"
)

// An ssh path holding a character a POSIX shell reads specially is refused,
// before the file is looked at, and not quoted: git runs core.sshCommand
// through a shell. So is a path that is not absolute.
//
// What it costs when this fails: a product sets core.sshCommand to a path
// git's shell splits, expands or runs a second command from, and every fetch
// over SSH runs something else, or fails.
func TestAnSSHPathAShellWouldReadIsRefused(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"C:/Program Files/OpenSSH/ssh.exe", "/opt/a'b/ssh", `/opt/a"b/ssh`, "/opt/$HOME/ssh",
		"/opt/`id`/ssh", "/opt/a;b/ssh", "/opt/a&b/ssh", "/opt/a|b/ssh", "/opt/a<b/ssh", "/opt/a>b/ssh",
		"/opt/a(b)/ssh", "/opt/*/ssh", "/opt/a?/ssh", "/opt/[a]/ssh", "/opt/#a/ssh", "/opt/~a/ssh",
		"/opt/a\nb/ssh", "/opt/a\tb/ssh", "ssh", "OpenSSH/ssh.exe"} {
		if got, err := agentSSHCommand(p); !errors.Is(err, fs.ErrInvalid) || got != "" {
			t.Errorf("agentSSHCommand(%q) = %q, %v, and want fs.ErrInvalid", p, got, err)
		}
	}
}
