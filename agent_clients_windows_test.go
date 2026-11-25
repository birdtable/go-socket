package gosocket_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/birdtable/go-socket/peer"
)

// agentClients are the OpenSSH clients a program meets on Windows: Git for
// Windows' (MSYS), the ssh git runs unless core.sshCommand or
// GIT_SSH_COMMAND names another, found in ProgramFiles, which Windows sets
// for every process; and Windows' own, Win32-OpenSSH, found where
// peer.AgentSSHCommand says its ssh is, so its answer is the client proven
// to list the agent's key.
//
// Git for Windows' ssh-add cannot open an agent on a Windows named pipe: it
// exits 2, "Error connecting to agent: Device or resource busy". Its row
// runs first, against a pipe whose
// instance waits fresh, so the refusal is not the busy pipe of an instance
// another client still holds.
func agentClients(t *testing.T) []agentClient {
	t.Helper()
	ssh, err := peer.AgentSSHCommand()
	if err != nil {
		t.Fatalf("peer.AgentSSHCommand: %v", err)
	}
	programs := os.Getenv("ProgramFiles") //nolint:forbidigo // cli.config.3: Windows' own folder, which Windows sets for every process; a library's test has no config package
	return []agentClient{
		{name: "Git for Windows", dir: filepath.Join(programs, "Git", "usr", "bin")},
		{name: "Win32-OpenSSH", dir: filepath.Dir(filepath.FromSlash(ssh)), lists: true},
	}
}
