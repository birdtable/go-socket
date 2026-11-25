package gosocket_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// directive is the comment that links a name of this module to another
// package's, unexported ones and the runtime's among them, written in two so
// this file holds no line that starts with it.
const directive = "//go:" + "linkname"

// No Go file of the module, of any system, a test's or a testdata file
// among them, holds a //go:linkname directive, at any column: every file,
// and no exception, as go-files holds its own.
//
// What it costs when this fails: a file links to the runtime's own system
// call, or to an unexported name of the standard library, and makes a call
// that no register row, no lint rule and no API check sees: a raw system
// call that the ban on unix.Syscall never meets, in any package.
func TestNoFileLinksANameOfAnotherPackage(t *testing.T) {
	t.Parallel()
	read := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		read++
		for _, g := range f.Comments {
			for _, c := range g.List {
				if strings.HasPrefix(c.Text, directive) {
					t.Errorf("%s holds %s: %s", path, directive, c.Text)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if read < 50 {
		t.Fatalf("read %d Go files, and the module has more: the walk missed its folders", read)
	}
}
