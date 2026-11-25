package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests read trees they write in t.TempDir. As in go-files, the package has no TestMain of
// testkit's: it is a tool the Taskfile runs, which imports the standard library alone and opens no
// socket.

// tree writes files, each a path from the root and its text, into a new folder and gives it.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// lines gives n lines of Go comment, each ended by a newline.
func lines(n int) string { return strings.Repeat("// a line\n", n) }

// What it costs when this fails: the size a tag is held to counts tests, test support or test
// data, or misses a system's file, and a tag passes or fails on lines no program links.
func TestSizeCountsTheNonTestLinesAsWcDoesAndRefusesAboveTheCeiling(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"a/a.go":           lines(1000),
		"a/b/b_windows.go": lines(Ceiling - 1003),
		"a/c.go":           lines(3) + "// no newline at the end",
		"a/a_test.go":      lines(5000),
		"a/testdata/t.go":  lines(5000),
		"internal/x/x.go":  lines(5000),
		".hidden/h.go":     lines(5000),
		"a/_draft.go":      lines(5000),
		"a/notes.txt":      lines(5000),
	}
	dir := tree(t, files)
	var out bytes.Buffer
	if code := run([]string{"size"}, dir, &out); code != 0 {
		t.Errorf("size exited %d at the ceiling, and want 0: %s", code, &out)
	}
	if want := fmt.Sprintf("non-test Go lines outside internal/: %d (the ceiling is %d)\n", Ceiling, Ceiling); out.String() != want {
		t.Errorf("size said %q, and want %q", &out, want)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "a.go"), []byte(lines(1001)), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"size"}, dir, &out); code != 1 {
		t.Errorf("size exited %d at %d lines, one above the ceiling, and want 1: %s", code, Ceiling+1, &out)
	}
	if want := fmt.Sprintf("%d lines is 1 above the ceiling", Ceiling+1); !strings.Contains(out.String(), want) {
		t.Errorf("size did not say %q:\n%s", want, &out)
	}
}

// What it costs when this fails: a task run with no word, a misspelt one or one measure no longer
// has (api and api-check went with API.txt) passes and checks nothing.
func TestRunTakesOneWordItKnows(t *testing.T) {
	t.Parallel()
	dir := tree(t, map[string]string{"a/a.go": "package a\n"})
	for _, args := range [][]string{nil, {"size", "size"}, {"sise"}, {"api"}, {"api-check"}} {
		var out bytes.Buffer
		if code := run(args, dir, &out); code != 2 {
			t.Errorf("run(%q) exited %d, and want 2: %s", args, code, &out)
		}
	}
}
