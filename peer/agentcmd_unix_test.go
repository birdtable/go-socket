//go:build unix

package peer

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sshDir is a folder of the test's under /tmp, whose path holds no character
// a shell reads specially, as a test's own temporary folder's may.
func sshDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "peer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// Off Windows, AgentSSHCommand is empty: the ssh git runs reads
// SSH_AUTH_SOCK's socket as DialAgent does.
//
// What it costs when this fails: a product on Unix sets core.sshCommand to a
// client it does not have, or another than the user's.
func TestAgentSSHCommandIsEmptyOffWindows(t *testing.T) {
	t.Parallel()
	if got, err := AgentSSHCommand(); got != "" || err != nil {
		t.Errorf("AgentSSHCommand() = %q, %v, and want \"\", nil", got, err)
	}
}

// An ssh that is there as a regular file is the command, as written; one that
// is not there, or is not a file, is no client: fs.ErrNotExist, naming it.
//
// What it costs when this fails: a product sets core.sshCommand to a client
// that is not installed, and every fetch over SSH fails, saying nothing of
// why.
func TestAnSSHThatIsNotThereIsNotExist(t *testing.T) {
	t.Parallel()
	dir := sshDir(t)
	ssh := filepath.Join(dir, "ssh")
	if err := os.WriteFile(ssh, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := agentSSHCommand(ssh); got != ssh || err != nil {
		t.Errorf("agentSSHCommand(%q) = %q, %v, and want it as written", ssh, got, err)
	}
	for _, p := range []string{filepath.Join(dir, "absent"), dir} {
		if got, err := agentSSHCommand(p); !errors.Is(err, fs.ErrNotExist) || got != "" || !strings.Contains(err.Error(), p) {
			t.Errorf("agentSSHCommand(%q) = %q, %v, and want fs.ErrNotExist naming it", p, got, err)
		}
	}
}
