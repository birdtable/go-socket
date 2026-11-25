package peer

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// shellSpecial holds each character a POSIX shell reads as more than itself
// in a word: git runs core.sshCommand and GIT_SSH_COMMAND through one, so a
// path holding any of them is refused, not quoted. A backslash and a tab are
// among them, though the form written has neither.
const shellSpecial = " \t\n'\"$`;&|<>()*?[#~\\"

// agentSSHCommand is the ssh at p, the client that reaches this system's
// agent, as git's core.sshCommand takes it: written with forward slashes,
// absolute, holding no character a shell reads specially, and a regular file
// that is there. Each check runs on the form written, before the file is
// looked at.
func agentSSHCommand(p string) (string, error) {
	cmd := filepath.ToSlash(p)
	if i := strings.IndexAny(cmd, shellSpecial); i >= 0 {
		return "", fmt.Errorf("the ssh that reaches the agent is %q, and git's shell would read its %q: %w", cmd, cmd[i], fs.ErrInvalid)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("the ssh that reaches the agent is %q, and it must be an absolute path: %w", cmd, fs.ErrInvalid)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("the ssh that reaches the agent, %s: %w", cmd, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("the ssh that reaches the agent, %s, is no file but a %s: %w", cmd, fi.Mode().Type(), fs.ErrNotExist)
	}
	return cmd, nil
}
