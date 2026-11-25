package gosocket_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// linkFolder puts at dir/tree/linked a junction to dir/outside: a reparse
// point any account may make (a symbolic link takes a privilege), which
// os.Lstat reports as irregular since Go 1.23. mklink runs in dir/tree with
// constant arguments, and makes the junction's target absolute itself.
func linkFolder(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", "linked", `..\outside`)
	cmd.Dir = filepath.Join(dir, "tree")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the junction: %v: %s", err, out)
	}
}
