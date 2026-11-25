//go:build unix

package caller

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The account that owns a planted socket is read as tailscaled's; with no
// socket nobody is named and nothing is wrong, and a file that is not a
// socket names nobody and says so.
//
// What it costs when this fails: a program trusts no tailscaled, or trusts
// whoever owns a plain file planted where the socket should be.
func TestTailscaledUIDIsTheOwnerOfAPlantedSocket(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("/tmp", "caller")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "tailscaled.sock")
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	held := 0
	if uid, ok, err := TailscaledUID(sock); err != nil || !ok || uid != uint32(os.Getuid()) {
		t.Errorf("a planted socket: %d %v %v, want %d", uid, ok, err, os.Getuid())
	}
	held++
	if uid, ok, err := TailscaledUID(filepath.Join(dir, "none.sock")); err != nil || ok || uid != 0 {
		t.Errorf("no socket: %d %v %v", uid, ok, err)
	}
	held++
	plain := filepath.Join(dir, "plain.sock")
	if err := os.WriteFile(plain, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if uid, ok, err := TailscaledUID(plain); err == nil || ok || uid != 0 {
		t.Errorf("a plain file: %d %v %v", uid, ok, err)
	}
	held++
	if held != 3 {
		t.Fatalf("held %d of 3 paths", held)
	}
}
