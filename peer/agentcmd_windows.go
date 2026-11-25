package peer

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// AgentSSHCommand is the ssh command that reaches the agent DialAgent dials
// on this system, in the form git takes for core.sshCommand and
// GIT_SSH_COMMAND; empty means git's own ssh does. On Windows it is Windows'
// own client, Win32-OpenSSH, <windows>/System32/OpenSSH/ssh.exe written with
// forward slashes, <windows> being the Windows folder as the system gives it:
// Git for Windows' own ssh cannot open an agent on a named pipe. A client
// that is not installed wraps fs.ErrNotExist, naming the path; a path holding
// a character a POSIX shell reads specially is refused, not quoted.
func AgentSSHCommand() (string, error) {
	dir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return "", fmt.Errorf("the Windows folder, where the ssh that reaches the agent is: %w", err)
	}
	return agentSSHCommand(filepath.Join(dir, "System32", "OpenSSH", "ssh.exe"))
}
