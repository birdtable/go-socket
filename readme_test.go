package gosocket_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTheREADMEShowsTheExampleAsItIs fails when the Go block in README.md is not Example in
// example_test.go, from func to its closing brace: the README's example is the function go test
// runs and checks against its Output line.
func TestTheREADMEShowsTheExampleAsItIs(t *testing.T) {
	t.Parallel()
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(readme), "\n```go\n")
	block, _, closed := strings.Cut(rest, "\n```\n")
	if !ok || !closed {
		t.Fatal("README.md has no Go block")
	}

	src, err := os.ReadFile("example_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "example_test.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var fn string
	for _, d := range f.Decls {
		if d, ok := d.(*ast.FuncDecl); ok && d.Recv == nil && d.Name.Name == "Example" {
			fn = string(src[fset.Position(d.Pos()).Offset:fset.Position(d.End()).Offset])
		}
	}
	if fn == "" {
		t.Fatal("example_test.go has no func Example")
	}
	if block != fn {
		t.Errorf("README.md's Go block is not Example in example_test.go:\n--- README.md\n%s\n--- example_test.go\n%s", block, fn)
	}
}

// link is a Markdown link's target, or an HTML src or srcset; code is a fenced block or a code
// span, where a call such as New[S, T](noun) reads as a link and is not one.
var (
	link = regexp.MustCompile(`\]\(([^)\s]+)\)|\bsrc(?:set)?="([^"]+)"`)
	code = regexp.MustCompile("(?s)```.*?```|`[^`]*`")
)

// TestEveryREADMELinkIntoTheTreeResolves fails for a link in a README.md, in the root or a
// package folder, in CONTRIBUTING.md or SECURITY.md, or in a note in docs/ if there is one, to a
// file or folder of the tree that is not there. A link that names a scheme or only an anchor is
// not the tree's and is not read.
func TestEveryREADMELinkIntoTheTreeResolves(t *testing.T) {
	t.Parallel()
	readmes, err := fs.Glob(os.DirFS("."), "*/README.md")
	if err != nil {
		t.Fatal(err)
	}
	notes, err := fs.Glob(os.DirFS("."), "docs/*.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range append(append([]string{"README.md", "CONTRIBUTING.md", "SECURITY.md"}, readmes...), notes...) {
		b, err := os.ReadFile(r)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, m := range link.FindAllStringSubmatch(code.ReplaceAllString(string(b), ""), -1) {
			target, _, _ := strings.Cut(m[1]+m[2], "#")
			if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			n++
			if _, err := os.Stat(filepath.Join(filepath.Dir(r), filepath.FromSlash(target))); err != nil {
				t.Errorf("%s links to %s, which is not in the tree: %v", r, m[1]+m[2], err)
			}
		}
		t.Logf("%s: %d links into the tree", r, n)
	}
}
