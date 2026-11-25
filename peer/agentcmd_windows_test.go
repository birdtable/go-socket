package peer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On Windows, AgentSSHCommand is Win32-OpenSSH's ssh.exe in the Windows
// folder: absolute, written with forward slashes, and a regular file that is
// there. The root package's agent test runs the ssh-add beside it against a
// served agent, so the answer is the client proven to list its key.
//
// What it costs when this fails: a product sets core.sshCommand to a path
// git's shell cannot take, or to no client, and git on Windows reaches no
// agent.
func TestAgentSSHCommandIsWindowsOwnSSH(t *testing.T) {
	t.Parallel()
	got, err := AgentSSHCommand()
	if err != nil {
		t.Fatalf("AgentSSHCommand: %v", err)
	}
	if !filepath.IsAbs(got) || strings.Contains(got, `\`) || !strings.HasSuffix(got, "/System32/OpenSSH/ssh.exe") {
		t.Errorf("AgentSSHCommand() = %q, and want <windows>/System32/OpenSSH/ssh.exe, absolute, with forward slashes", got)
	}
	if fi, err := os.Stat(got); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("AgentSSHCommand() = %q, which is %v, %v, and want a regular file", got, fi, err)
	}
}
