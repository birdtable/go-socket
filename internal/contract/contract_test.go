package contract_test

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// root is the module's folder, two up from this package's.
const root = "../.."

// heldTo is the four systems the library is held to, which "all" in a line's systems names.
func heldTo() []string { return []string{"darwin", "freebsd", "linux", "windows"} }

// promise is one line of api/contract.txt.
type promise struct {
	pkg, test string
	systems   []string
	sentence  string
	raw       string
}

// key is what the index is sorted by: the package, then the test.
func (p promise) key() string { return p.pkg + "\t" + p.test }

// TestThePromiseIndexHoldsItsLines is the gate: api/contract.txt is well formed and sorted, every
// line's test is in its package, built on each system the line names and reaching no skip there,
// and a line removed since the last tag is in that tag's api/breaks/<tag>.contract.txt, and no
// other line is.
//
// What it costs when this fails: a product relies on a promise no run proves on its system, or a
// promise a product relied on is gone with no word of it in the tag that drops it.
func TestThePromiseIndexHoldsItsLines(t *testing.T) {
	t.Parallel()
	index := read(t, filepath.Join(root, "api", "contract.txt"))
	lines, problems := parseIndex("api/contract.txt", index)
	problems = append(problems, checkTests(root, lines)...)

	if tag, ok := baseTag(t); !ok {
		t.Log("no tag is reachable from HEAD, so no line is checked as removed since one")
	} else if old, ok := atTag(t, tag, "api/contract.txt"); !ok {
		t.Logf("%s holds no api/contract.txt, so no line is checked as removed since it", tag)
	} else {
		breaks, err := os.ReadFile(filepath.Join(root, "api", "breaks", tag+".contract.txt"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		problems = append(problems, checkRemoved(tag, old, index, breaks)...)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// read is the file at path, or the test fails.
func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var (
	testName = regexp.MustCompile(`^Test[A-Z0-9_][A-Za-z0-9_]*$`)
	pkgPath  = regexp.MustCompile(`^[a-z0-9_]+(/[a-z0-9_]+)*$`)
)

// lines splits a file of tab-separated lines, each ended by a newline, into its lines, or says
// why it is no such file.
func lines(name string, data []byte) ([]string, []string) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, []string{name + " does not end in a newline"}
	}
	return strings.Split(string(data[:len(data)-1]), "\n"), nil
}

// parseIndex reads an index or a break file, named name, and says what is wrong with each line,
// and with the order: sorted by package and test, with no key twice.
func parseIndex(name string, data []byte) ([]promise, []string) {
	raws, problems := lines(name, data)
	var ps []promise
	for i, raw := range raws {
		p, err := parseLine(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s:%d: %v", name, i+1, err))
			continue
		}
		if n := len(ps); n > 0 && ps[n-1].key() >= p.key() {
			problems = append(problems, fmt.Sprintf("%s:%d: %s %s is not after %s %s: the file is sorted by package and test, each once", name, i+1, p.pkg, p.test, ps[n-1].pkg, ps[n-1].test))
		}
		ps = append(ps, p)
	}
	return ps, problems
}

// parseLine reads one line: the package, the test, the systems and the promise, tab-separated.
func parseLine(raw string) (promise, error) {
	f := strings.Split(raw, "\t")
	if len(f) != 4 {
		return promise{}, fmt.Errorf("%d tab-separated fields, want 4: %q", len(f), raw)
	}
	p := promise{pkg: f[0], test: f[1], sentence: f[3], raw: raw}
	switch {
	case !pkgPath.MatchString(p.pkg):
		return p, fmt.Errorf("the package %q is not a folder of this module", p.pkg)
	case !testName.MatchString(p.test):
		return p, fmt.Errorf("%q is not a test's name", p.test)
	case strings.TrimSpace(p.sentence) != p.sentence || !strings.HasSuffix(p.sentence, ".") || strings.Contains(p.sentence[:len(p.sentence)-1], ". "):
		return p, fmt.Errorf("the promise %q is not one sentence, ended by a full stop", p.sentence)
	}
	if f[2] == "all" {
		p.systems = heldTo()
		return p, nil
	}
	for s := range strings.SplitSeq(f[2], ",") {
		if !slices.Contains(heldTo(), s) || slices.Contains(p.systems, s) {
			return p, fmt.Errorf("the systems %q are not all, or some of %s each once", f[2], strings.Join(heldTo(), ", "))
		}
		p.systems = append(p.systems, s)
	}
	return p, nil
}

// fn is one function of a package's tests, in the file it is in.
type fn struct {
	file string
	decl *ast.FuncDecl
}

// pkgFuncs is every function in a package's test files, whatever their build constraints, by name,
// a method by its own name.
func pkgFuncs(fset *token.FileSet, dir string) (map[string][]fn, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	funcs := map[string][]fn{}
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			if d, ok := d.(*ast.FuncDecl); ok && d.Body != nil {
				funcs[d.Name.Name] = append(funcs[d.Name.Name], fn{filepath.Base(name), d})
			}
		}
	}
	return funcs, nil
}

// builds says whether the file name in dir is built for goos, cgo on, as the tiers build tests.
func builds(dir, name, goos string) bool {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = goos, "amd64", true
	ok, err := ctx.MatchFile(dir, name)
	return err == nil && ok
}

// checkTests says, for each line, where its test is missing, not built on a system the line
// names, or reaches a skip there: a call of Skip, Skipf or SkipNow in the test or in any function
// of the package's tests built there that it names, followed as far as names reach. A skip behind
// a check of the system is counted all the same: the gate reads names, not values.
func checkTests(root string, ps []promise) []string {
	fset := token.NewFileSet()
	byPkg := map[string]map[string][]fn{}
	var problems []string
	for _, p := range ps {
		dir := filepath.Join(root, filepath.FromSlash(p.pkg))
		funcs, ok := byPkg[p.pkg]
		if !ok {
			var err error
			if funcs, err = pkgFuncs(fset, dir); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", p.pkg, err))
			}
			byPkg[p.pkg] = funcs
		}
		tests := slices.DeleteFunc(slices.Clone(funcs[p.test]), func(f fn) bool { return f.decl.Recv != nil })
		if len(tests) == 0 {
			problems = append(problems, fmt.Sprintf("%s %s: no such test in %s's test files", p.pkg, p.test, p.pkg))
			continue
		}
		for _, goos := range p.systems {
			problems = append(problems, checkOn(fset, dir, funcs, p, tests, goos)...)
		}
	}
	return problems
}

// checkOn is checkTests for one line and one system.
func checkOn(fset *token.FileSet, dir string, funcs map[string][]fn, p promise, tests []fn, goos string) []string {
	on := func(fs []fn) []fn {
		return slices.DeleteFunc(slices.Clone(fs), func(f fn) bool { return !builds(dir, f.file, goos) })
	}
	queue := on(tests)
	if len(queue) == 0 {
		return []string{fmt.Sprintf("%s %s is not built on %s, which the line names", p.pkg, p.test, goos)}
	}
	var problems []string
	seen := map[*ast.FuncDecl]bool{}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		if seen[f.decl] {
			continue
		}
		seen[f.decl] = true
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && slices.Contains([]string{"Skip", "Skipf", "SkipNow"}, sel.Sel.Name) {
					problems = append(problems, fmt.Sprintf("%s %s can skip on %s, which the line names: %s calls %s at %s", p.pkg, p.test, goos, f.decl.Name.Name, sel.Sel.Name, fset.Position(n.Pos())))
				}
			case *ast.Ident:
				queue = append(queue, on(funcs[n.Name])...)
			}
			return true
		})
	}
	return problems
}

// gains says whether now is was with no more than systems added: the same package, test and
// sentence, and every system of was among now's.
func gains(was, now promise) bool {
	if was.key() != now.key() || was.sentence != now.sentence {
		return false
	}
	for _, s := range was.systems {
		if !slices.Contains(now.systems, s) {
			return false
		}
	}
	return true
}

// checkRemoved says, both ways, where the break file of tag and the index differ: a line of the
// index at tag that is not in it now, and is not now that line with systems added, must be in the
// break file; a line of the break file must have been in the index at tag and be in it no more.
func checkRemoved(tag string, old, cur, breaks []byte) []string {
	name := "api/breaks/" + tag + ".contract.txt"
	was, _ := parseIndex(tag+":api/contract.txt", old)
	now, _ := parseIndex("api/contract.txt", cur)
	listed, problems := parseIndex(name, breaks)
	isNow := map[string]bool{}
	for _, p := range now {
		isNow[p.raw] = true
	}
	for _, w := range was {
		if isNow[w.raw] || slices.ContainsFunc(now, func(n promise) bool { return gains(w, n) }) {
			continue
		}
		if !slices.ContainsFunc(listed, func(l promise) bool { return l.raw == w.raw }) {
			problems = append(problems, fmt.Sprintf("%s %s was promised at %s and is gone, and %s does not list it: %q", w.pkg, w.test, tag, name, w.raw))
		}
	}
	for _, l := range listed {
		switch {
		case isNow[l.raw]:
			problems = append(problems, fmt.Sprintf("%s lists %s %s, which api/contract.txt still holds", name, l.pkg, l.test))
		case !slices.ContainsFunc(was, func(w promise) bool { return w.raw == l.raw }):
			problems = append(problems, fmt.Sprintf("%s lists %s %s, which api/contract.txt did not hold so at %s", name, l.pkg, l.test, tag))
		}
	}
	return problems
}

// gitCmd is git with args in the module's folder, with no GIT_ variable a hook may set, and no
// global or system config read.
func gitCmd(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // G204: git, with the test's own arguments and the tag git describe gave
	cmd.Dir = root
	var env []string
	for _, kv := range cmd.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	return cmd
}

// baseTag is the last tag reachable from HEAD, found as the API gate finds it, and false where
// there is none.
func baseTag(t *testing.T) (string, bool) {
	t.Helper()
	out, err := gitCmd(t, "describe", "--tags", "--abbrev=0", "--match", "v*").CombinedOutput()
	if errors.Is(err, exec.ErrNotFound) {
		t.Fatal(err)
	}
	if err != nil {
		t.Logf("git describe: %v: %s", err, bytes.TrimSpace(out))
		return "", false
	}
	return string(bytes.TrimSpace(out)), true
}

// atTag is the file path as tag holds it, and false where tag holds none.
func atTag(t *testing.T, tag, path string) ([]byte, bool) {
	t.Helper()
	if err := gitCmd(t, "cat-file", "-e", tag+":"+path).Run(); err != nil {
		return nil, false
	}
	out, err := gitCmd(t, "show", tag+":"+path).Output()
	if err != nil {
		t.Fatalf("git show %s:%s: %v", tag, path, err)
	}
	return out, true
}

// The gate is red where it should be: for each break, the table writes a package's test files
// and an index in a folder of its own, and the problem the gate gives must name the break; the
// lines that hold give none.
//
// What it costs when this fails: the gate passes an index whose promise no test proves on a
// system it names, or a promise dropped with no word of it.
func TestTheGateIsRedForEachBreak(t *testing.T) {
	t.Parallel()
	const kiosk = `package kiosk

import "testing"

func TestPress(t *testing.T) { warm(t) }

func TestFeed(t *testing.T) { t.Log("fed") }

func warm(t *testing.T) { cold(t) }
`
	files := map[string]string{
		"kiosk_test.go":         kiosk,
		"cold_unix_test.go":     "//go:build unix\n\npackage kiosk\n\nimport \"testing\"\n\nfunc cold(t *testing.T) { t.Skip(\"the fuser is cold\") }\n",
		"cold_windows_test.go":  "package kiosk\n\nimport \"testing\"\n\nfunc cold(t *testing.T) { t.Log(\"warm\") }\n",
		"stamp_windows_test.go": "package kiosk\n\nimport \"testing\"\n\nfunc TestStamp(t *testing.T) { t.Log(\"stamped\") }\n",
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "kiosk"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, "kiosk", name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	line := func(test, systems string) string { return "kiosk\t" + test + "\t" + systems + "\tIt prints.\n" }
	for _, c := range []struct {
		name, index string
		red         string // a pattern the problems must match, or "" for none
	}{
		{"a test that is there, run on every system", line("TestFeed", "all"), ""},
		{"a test whose skip is on another system alone", line("TestPress", "windows"), ""},
		{"a test named that is not there", line("TestFold", "linux"), `kiosk TestFold: no such test`},
		{"a test that reaches a skip on a system named", line("TestPress", "darwin,windows"), `kiosk TestPress can skip on darwin, which the line names: cold calls Skip at .*cold_unix_test\.go:7`},
		{"a test not built on a system named", line("TestStamp", "linux"), `kiosk TestStamp is not built on linux`},
		{"an unsorted file", line("TestPress", "windows") + line("TestFeed", "linux"), `:2: kiosk TestFeed is not after kiosk TestPress`},
		{"a line twice", line("TestFeed", "linux") + line("TestFeed", "linux"), `:2: kiosk TestFeed is not after kiosk TestFeed`},
		{"a system the library is not held to", line("TestFeed", "plan9"), `the systems "plan9" are not all`},
		{"two promises in one line", "kiosk\tTestFeed\tall\tIt prints. It folds.\n", `is not one sentence`},
		{"three fields", "kiosk\tTestFeed\tIt prints.\n", `3 tab-separated fields, want 4`},
		{"no newline at the end", strings.TrimSuffix(line("TestFeed", "all"), "\n"), `does not end in a newline`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ps, problems := parseIndex("api/contract.txt", []byte(c.index))
			problems = append(problems, checkTests(dir, ps)...)
			holds(t, problems, c.red)
		})
	}
}

// The removed-line check is red both ways, and lets a line gain a system: a line in the index at
// the base tag and gone now is red until the break file lists it as it stood; a line the break
// file lists that is still in the index, or never was in it, is red too.
//
// What it costs when this fails: a tag drops a promise a product relied on with no word of it, or
// the break file names a removal that did not happen, and a product drops a call it may keep.
func TestARemovedLineIsRedUntilItsBreakFileListsIt(t *testing.T) {
	t.Parallel()
	feed := "kiosk\tTestFeed\tlinux\tIt feeds.\n"
	press := "kiosk\tTestPress\tlinux\tIt presses.\n"
	for _, c := range []struct {
		name, old, cur, breaks, red string
	}{
		{"nothing removed", feed + press, feed + press, "", ""},
		{"a line removed and listed", feed + press, feed, press, ""},
		{"a line that only gains a system", feed + press, feed + "kiosk\tTestPress\tlinux,windows\tIt presses.\n", "", ""},
		{"a line that gains all", feed + press, feed + "kiosk\tTestPress\tall\tIt presses.\n", "", ""},
		{"a removed line with no break entry", feed + press, feed, "", `kiosk TestPress was promised at v0\.6\.1 and is gone, and api/breaks/v0\.6\.1\.contract\.txt does not list it`},
		{"a line whose promise changed", feed + press, feed + "kiosk\tTestPress\tlinux\tIt presses twice.\n", "", `kiosk TestPress was promised at v0\.6\.1 and is gone`},
		{"a line that loses a system", feed + "kiosk\tTestPress\tlinux,windows\tIt presses.\n", feed + press, "", `kiosk TestPress was promised at v0\.6\.1 and is gone`},
		{"a listed line still in the index", feed + press, feed + press, press, `lists kiosk TestPress, which api/contract.txt still holds`},
		{"a listed line never in the index", feed, feed, press, `lists kiosk TestPress, which api/contract.txt did not hold so at v0\.6\.1`},
		{"a listed line not as it stood", feed + press, feed, "kiosk\tTestPress\tlinux\tIt pressed.\n", `kiosk TestPress was promised at v0\.6\.1 and is gone`},
		{"an unsorted break file", feed + press, "", press + feed, `:2: kiosk TestFeed is not after kiosk TestPress`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			holds(t, checkRemoved("v0.6.1", []byte(c.old), []byte(c.cur), []byte(c.breaks)), c.red)
		})
	}
}

// holds fails the test unless problems is empty where red is "", or one of them matches red.
func holds(t *testing.T, problems []string, red string) {
	t.Helper()
	if red == "" {
		if len(problems) > 0 {
			t.Errorf("want no problem, got:\n%s", strings.Join(problems, "\n"))
		}
		return
	}
	re := regexp.MustCompile(red)
	if !slices.ContainsFunc(problems, re.MatchString) {
		t.Errorf("want a problem matching %q, got:\n%s", red, strings.Join(problems, "\n"))
	}
}
