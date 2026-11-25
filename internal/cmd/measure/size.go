package main

import (
	"bytes"
	"io/fs"
	"strings"
)

// Ceiling is the most non-test Go lines the module's packages may hold at a tag: the count at
// v0.3.0's final head, set by measure, and moved only by what a pull request measures.
const Ceiling = 3627

// Size counts the non-test Go lines of the module's packages the way `wc -l` of their files
// does, one per newline: every .go file not named _test.go, outside internal/, testdata and
// any folder whose name starts with a dot.
func Size(fsys fs.FS) (int, error) {
	n := 0
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipped(name) {
				return fs.SkipDir
			}
			return nil
		}
		if !source(name) {
			return nil
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		n += bytes.Count(b, []byte("\n"))
		return nil
	})
	return n, err
}

// skipped says whether the folder name, a path from the module's root, holds no package of the
// module's surface: internal/, testdata, and a folder whose name starts with a dot.
func skipped(name string) bool {
	if name == "." {
		return false
	}
	base := name[strings.LastIndex(name, "/")+1:]
	return name == "internal" || base == "testdata" || strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_")
}

// source says whether the file name is a Go file a build of the package reads.
func source(name string) bool {
	base := name[strings.LastIndex(name, "/")+1:]
	return strings.HasSuffix(base, ".go") && !strings.HasSuffix(base, "_test.go") &&
		!strings.HasPrefix(base, ".") && !strings.HasPrefix(base, "_")
}
