package gosocket_test

import (
	"fmt"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestLintReadsEverySystemTheModuleBuildsFor holds SYSTEMS in Taskfile.yml,
// the systems lint reads and systems builds and vets, to every system the
// module builds for. Each port of each GOOS out of the list is built with cgo
// off, as systems builds, and must fail; a port SYSTEMS names must be one of
// go tool dist list's. One system of the list is built first, so a build that
// fails for want of a module or a toolchain is not taken for one the module
// cannot build for.
//
// What it costs when this fails: a file tagged for a system lint does not
// read can call what the linter refuses everywhere else.
func TestLintReadsEverySystemTheModuleBuildsFor(t *testing.T) {
	t.Parallel()
	ports := goPorts(t)
	listed := lintedSystems(t)
	for goos, goarch := range listed {
		if !slices.Contains(ports[goos], goarch) {
			t.Errorf("SYSTEMS in Taskfile.yml names %s/%s, which is not a port go tool dist list names", goos, goarch)
		}
	}
	if out, err := goBuild(t, "linux", listed["linux"]).CombinedOutput(); err != nil {
		t.Fatalf("go build ./... for linux/%s, a system SYSTEMS names: %v: %s", listed["linux"], err, out)
	}
	for goos, arches := range ports {
		if _, ok := listed[goos]; ok {
			continue
		}
		for _, goarch := range arches {
			t.Run(goos+"/"+goarch, func(t *testing.T) {
				t.Parallel()
				if goBuild(t, goos, goarch).Run() == nil {
					t.Errorf("the module builds for %s/%s, and SYSTEMS in Taskfile.yml does not name %s, so lint does not read it", goos, goarch, goos)
				}
			})
		}
	}
}

// TestNoFileLintCannotRead refuses every file in the tree that the go command
// can build into the module and golangci-lint does not read; treeRefusals
// says which.
//
// What it costs when this fails: an assembly file can make a system call, or
// jump to a symbol of the runtime, with no linkname for linkname_test.go to see;
// a file no lint run reads, or a folder linked in from outside the tree, is
// read by no linter at all.
func TestNoFileLintCannotRead(t *testing.T) {
	t.Parallel()
	for path, reasons := range treeRefusals(t, ".") {
		for _, r := range reasons {
			t.Errorf("%s %s", path, r)
		}
	}
}

// TestTheTreeCheckRefusesEachKind plants, in a tree of its own, one file of
// each kind treeRefusals refuses and one of each kind it lets through, so a
// change to the check that lets a kind through fails here by the kind's name,
// on every system the tests run on.
func TestTheTreeCheckRefusesEachKind(t *testing.T) {
	t.Parallel()
	const pkg = "package p\n"
	const notRead = "is read by no lint run"
	// Each plant refused, and a word of the reason it must give.
	type plant struct{ src, why string }
	refused := map[string]plant{
		"x_amd64.s":    {"", "is a .s file"},
		"x.syso":       {"", "is a .syso file"},
		"x.c":          {"", "is a .c file"},
		"imports_c.go": {pkg + "\nimport \"C\"\n", "imports C"},
		// Named for a GOOS out of SYSTEMS.
		"x_ios.go": {pkg, "is named for ios"},
		// A constraint naming a GOOS out of SYSTEMS, though linux reads it.
		"or_js.go": {"//go:build linux || js\n\n" + pkg, "which names js"},
		// darwin is linted at amd64 alone, so no run reads it, and every
		// Apple-silicon Mac builds it.
		"x_darwin_arm64.go": {pkg, notRead},
		// An arch no run reads, in the name and in a constraint. (linux &&
		// arm64 would pass: android satisfies linux, and is linted at arm64.)
		"x_riscv64.go":    {pkg, notRead},
		"freebsdarm64.go": {"//go:build freebsd && arm64\n\n" + pkg, notRead},
		// Lint runs with cgo off and without -race.
		"cgo.go":  {"//go:build cgo\n\n" + pkg, notRead},
		"race.go": {"//go:build race\n\n" + pkg, notRead},
		// A constraint no system satisfies.
		"never.go": {"//go:build linux && windows\n\n" + pkg, notRead},
	}
	passed := map[string]string{
		"plain.go":            pkg,
		"x_linux.go":          pkg,
		"x_amd64.go":          pkg,
		"x_android_arm64.go":  pkg,
		"x_windows_test.go":   pkg,
		"aix.go":              "//go:build aix && ppc64\n\n" + pkg,
		"unix_no_cgo_race.go": "//go:build unix && !cgo && !race\n\n" + pkg,
	}
	dir := t.TempDir()
	tree := filepath.Join(dir, "tree")
	outside := filepath.Join(dir, "outside")
	for _, d := range []string{tree, outside} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, src string) {
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, p := range refused {
		write(filepath.Join(tree, name), p.src)
	}
	for name, src := range passed {
		write(filepath.Join(tree, name), src)
	}
	// A folder linked in from outside the tree, by linkFolder: a junction on
	// Windows and a symbolic link elsewhere. What it holds would be refused,
	// so a walk that follows the link fails below by that path.
	write(filepath.Join(outside, "y_amd64.s"), "")
	linkFolder(t, dir)
	refused["linked"] = plant{why: "is neither a file nor a folder"}
	got := treeRefusals(t, tree)
	for name, p := range refused {
		reasons := got[filepath.Join(tree, name)]
		if !slices.ContainsFunc(reasons, func(r string) bool { return strings.Contains(r, p.why) }) {
			t.Errorf("%s gave the reasons %q, and want one that says %q", name, reasons, p.why)
		}
	}
	for path, reasons := range got {
		if _, ok := refused[strings.TrimPrefix(path, tree+string(filepath.Separator))]; !ok {
			t.Errorf("%s was refused, and want it let through or never reached: %q", path, reasons)
		}
	}
}

// treeRefusals walks the tree at root and returns, for each path it refuses,
// why. It refuses every file the go command can build into the module and
// golangci-lint does not read: assembly (.s, .S, .sx), C, C++, Objective-C,
// Fortran and SWIG sources and headers, .syso objects, and the Go files
// goFileRefusals refuses. It refuses every entry that is neither a file nor a
// folder: a symbolic link, a Windows junction (irregular since Go 1.23) or any
// other reparse point, a device, a pipe or a socket. Neither this walk nor
// ./... follows a link, so lint does not read a package linked in, and the go
// command builds it for any package that imports it.
func treeRefusals(t *testing.T, root string) map[string][]string {
	t.Helper()
	ports := goPorts(t)
	listed := lintedSystems(t)
	unlinted := func(goos string) bool {
		_, known := ports[goos]
		_, ok := listed[goos]
		return known && !ok
	}
	refusals := map[string][]string{}
	refuse := func(path, why string) { refusals[path] = append(refusals[path], why) }
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		// Both the entry's type and Lstat's: either one naming more than a
		// folder refuses it.
		if typ := d.Type() | info.Mode().Type(); typ&^fs.ModeDir != 0 {
			refuse(path, fmt.Sprintf("is neither a file nor a folder (%v): the walk does not follow it and ./... does not list what it holds, so lint does not read a package linked in, and the go command builds it for any package that imports it", typ))
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == filepath.Join(root, ".git") {
				return filepath.SkipDir
			}
			return nil
		}
		if refusedExtension(filepath.Ext(path)) {
			refuse(path, fmt.Sprintf("is a %s file: the go command builds it into the module, and golangci-lint does not read it", filepath.Ext(path)))
			return nil
		}
		if filepath.Ext(path) == ".go" {
			for _, why := range goFileRefusals(path, listed, unlinted) {
				refuse(path, why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return refusals
}

// refusedExtension reports whether ext is one go/build takes into a package
// besides .go.
func refusedExtension(ext string) bool {
	switch ext {
	case ".s", ".S", ".sx",
		".c", ".cc", ".cpp", ".cxx", ".m",
		".h", ".hh", ".hpp", ".hxx",
		".f", ".F", ".for", ".f90",
		".swig", ".swigcxx", ".syso":
		return true
	}
	return false
}

// goFileRefusals returns why the Go file at path is refused, or nothing. It
// refuses a file no lint run reads: one whose name and build constraint
// match, by go/build, the tags of no entry of SYSTEMS, each run being its
// GOOS and GOARCH, unix where it holds, gc, the release tags, cgo off and race
// off. No GOARCH feature tag (amd64.v1) and no
// GOEXPERIMENT tag is set, though lint's runs hold some, so a file that needs
// one is refused: that only refuses more. Besides that, it refuses a file that
// imports C, which lint skips with cgo off, and a file whose name or build
// constraint names, under a ! or not, a GOOS that SYSTEMS does not.
func goFileRefusals(path string, listed map[string]string, unlinted func(string) bool) []string {
	var refusals []string
	src, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("cannot be read: %v", err)}
	}
	if goos := fileNameGOOS(filepath.Base(path), unlinted); goos != "" {
		refusals = append(refusals, fmt.Sprintf("is named for %s, which SYSTEMS in Taskfile.yml does not name, so lint does not read it", goos))
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return append(refusals, fmt.Sprintf("cannot be parsed: %v", err))
	}
	for _, imp := range f.Imports {
		if imp.Path.Value == `"C"` {
			refusals = append(refusals, "imports C: lint runs with cgo off, so it does not read the file")
		}
	}
	var lines []string
	for _, group := range f.Comments {
		if group.Pos() > f.Package {
			break
		}
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) && !constraint.IsPlusBuild(c.Text) {
				continue
			}
			lines = append(lines, c.Text)
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				refusals = append(refusals, fmt.Sprintf("has the build constraint %q: %v", c.Text, err))
				continue
			}
			for _, tag := range constraintTags(expr) {
				if unlinted(tag) {
					refusals = append(refusals, fmt.Sprintf("has the build constraint %q, which names %s, and SYSTEMS in Taskfile.yml does not name it", c.Text, tag))
				}
			}
		}
	}
	read, err := lintReads(path, listed)
	if err != nil {
		return append(refusals, fmt.Sprintf("cannot be matched against SYSTEMS: %v", err))
	}
	if !read {
		said := "no build constraint"
		if len(lines) > 0 {
			said = fmt.Sprintf("the build constraint %q", strings.Join(lines, " "))
		}
		refusals = append(refusals, fmt.Sprintf("is read by no lint run: its name %s and %s match no entry of SYSTEMS in Taskfile.yml with cgo and race off", filepath.Base(path), said))
	}
	return refusals
}

// lintReads reports whether some lint run reads the Go file at path: whether
// go/build matches its name and build constraint for some entry of SYSTEMS,
// with cgo off and no race tag.
func lintReads(path string, listed map[string]string) (bool, error) {
	dir, name := filepath.Split(path)
	for goos, goarch := range listed {
		ctxt := build.Context{
			GOOS:        goos,
			GOARCH:      goarch,
			Compiler:    "gc",
			CgoEnabled:  false,
			ReleaseTags: build.Default.ReleaseTags,
		}
		ok, err := ctxt.MatchFile(dir, name)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// constraintTags returns every tag a build constraint names, under a ! or
// not. Eval would not do: it skips the right of an && whose left is false.
func constraintTags(x constraint.Expr) []string {
	switch x := x.(type) {
	case *constraint.TagExpr:
		return []string{x.Tag}
	case *constraint.NotExpr:
		return constraintTags(x.X)
	case *constraint.AndExpr:
		return append(constraintTags(x.X), constraintTags(x.Y)...)
	case *constraint.OrExpr:
		return append(constraintTags(x.X), constraintTags(x.Y)...)
	}
	return nil
}

// fileNameGOOS returns the GOOS a Go file's name holds it to, by go/build's
// rule, when unlinted is true for it, and "" otherwise. Only the words after
// the first underscore count, and a last word of test is dropped; the GOOS is
// then the last word, or the one before a last word that is a GOARCH. A word
// is taken as a GOOS here whenever unlinted is true for it, which is wider
// than go/build reads, so it only refuses more.
func fileNameGOOS(name string, unlinted func(string) bool) string {
	name = strings.TrimSuffix(name, ".go")
	_, rest, ok := strings.Cut(name, "_")
	if !ok {
		return ""
	}
	words := strings.Split(rest, "_")
	if words[len(words)-1] == "test" {
		words = words[:len(words)-1]
	}
	for i := len(words) - 1; i >= 0 && i >= len(words)-2; i-- {
		if unlinted(words[i]) {
			return words[i]
		}
	}
	return ""
}

// lintedSystems reads SYSTEMS from Taskfile.yml, as GOOS to GOARCH. It fails
// the test on a word that is not GOOS/GOARCH, or a GOOS named twice.
func lintedSystems(t *testing.T) map[string]string {
	t.Helper()
	src, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatalf("reading Taskfile.yml: %v", err)
	}
	m := regexp.MustCompile(`(?m)^  SYSTEMS: (.+)$`).FindAllSubmatch(src, -1)
	if len(m) != 1 {
		t.Fatalf("Taskfile.yml has %d SYSTEMS lines, and want one", len(m))
	}
	word := regexp.MustCompile(`^([a-z0-9]+)/([a-z0-9]+)$`)
	listed := map[string]string{}
	for sys := range strings.FieldsSeq(string(m[0][1])) {
		parts := word.FindStringSubmatch(sys)
		if parts == nil {
			t.Fatalf("SYSTEMS in Taskfile.yml names %q, and want GOOS/GOARCH", sys)
		}
		if _, twice := listed[parts[1]]; twice {
			t.Fatalf("SYSTEMS in Taskfile.yml names %s twice", parts[1])
		}
		listed[parts[1]] = parts[2]
	}
	return listed
}

// goPorts returns go tool dist list's ports, as GOOS to its GOARCHes.
func goPorts(t *testing.T) map[string][]string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "tool", "dist", "list")
	cmd.Env = append(cmd.Environ(), "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go tool dist list: %v", err)
	}
	ports := map[string][]string{}
	for line := range strings.Lines(string(out)) {
		goos, goarch, ok := strings.Cut(strings.TrimSpace(line), "/")
		if !ok {
			t.Fatalf("go tool dist list printed %q, and want GOOS/GOARCH", line)
		}
		ports[goos] = append(ports[goos], goarch)
	}
	return ports
}

// goBuild is go build ./... for goos and goarch with cgo off, as systems runs
// it, with no fetch.
func goBuild(t *testing.T, goos, goarch string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "build", "./...")
	cmd.Env = append(cmd.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=-mod=readonly")
	return cmd
}
