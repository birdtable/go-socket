//go:build !windows

package peer

// AgentSSHCommand is the ssh command that reaches the agent DialAgent dials
// on this system, in the form git takes for core.sshCommand and
// GIT_SSH_COMMAND; empty means git's own ssh does. Off Windows it is empty:
// the ssh git runs reads SSH_AUTH_SOCK's socket as DialAgent does. On
// Windows it is Windows' own client, Win32-OpenSSH's ssh.exe.
func AgentSSHCommand() (string, error) {
	return "", nil
}
