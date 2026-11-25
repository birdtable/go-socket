//go:build !windows

package gosocket_test

import (
	"os"
	"path/filepath"
	"testing"
)

// linkFolder puts at dir/tree/linked a symbolic link to dir/outside.
func linkFolder(t *testing.T, dir string) {
	t.Helper()
	if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(dir, "tree", "linked")); err != nil {
		t.Fatal(err)
	}
}
