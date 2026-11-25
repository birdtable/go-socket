package gosocket_test

import (
	"bytes"
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// noHistory is the start of the line the secrets task says when it fails for want of history.
const noHistory = "secrets: no history to scan: "

// TestSecretsFailsWhereTheTreeHasNoHistoryOfItsOwn runs the secrets task, as Taskfile.yml has
// it, in copies of this tree: one with no .git (as a tree taken from a tarball), one inside a
// folder that is a repository with a commit of its own, one in a repository with no commit, and
// one in a repository with the tree committed. gitleaks git passes the first three having
// scanned no commit of the tree's own; the task must fail them, saying why, and pass the last.
// It runs silent, so a line in what it says is the task's own and not a command it echoes.
func TestSecretsFailsWhereTheTreeHasNoHistoryOfItsOwn(t *testing.T) {
	t.Parallel()
	bin := taskBin(t)
	for _, c := range []struct {
		name string
		// make lays the tree out under root, and says the folder the task runs in.
		make func(t *testing.T, root string) string
		pass bool
	}{
		{"a tree with no .git", func(t *testing.T, root string) string {
			t.Helper()
			copyTree(t, root)
			return root
		}, false},
		{"a tree inside a folder that is a repository with a commit", func(t *testing.T, root string) string {
			t.Helper()
			git(t, root, exec.CommandContext(t.Context(), "git", "init", "-q"))
			git(t, root, exec.CommandContext(t.Context(), "git", "-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "the folder's"))
			tree := filepath.Join(root, "tree")
			copyTree(t, tree)
			return tree
		}, false},
		{"a tree in a repository with no commit", func(t *testing.T, root string) string {
			t.Helper()
			copyTree(t, root)
			git(t, root, exec.CommandContext(t.Context(), "git", "init", "-q"))
			return root
		}, false},
		{"a tree committed in a repository of its own", func(t *testing.T, root string) string {
			t.Helper()
			copyTree(t, root)
			git(t, root, exec.CommandContext(t.Context(), "git", "init", "-q"))
			git(t, root, exec.CommandContext(t.Context(), "git", "add", "-A"))
			git(t, root, exec.CommandContext(t.Context(), "git", "-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "the tree"))
			return root
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := c.make(t, t.TempDir())
			cmd := goTask(t, bin, "--silent", "secrets")
			cmd.Dir = dir
			isolate(cmd)
			out, err := cmd.CombinedOutput()
			said := saysNoHistory(out)
			switch {
			case c.pass && (err != nil || said):
				t.Fatalf("%s: task secrets: %v, and want it to pass:\n%s", c.name, err, out)
			case !c.pass && (err == nil || !said):
				t.Fatalf("%s: task secrets: %v, and want it to fail with %q:\n%s", c.name, err, noHistory, out)
			}
		})
	}
}

// upToDate is the line go-task says when it skips fmt.
const upToDate = `task: Task "fmt" is up to date`

// TestAMemberKilledBeforeItsStampRunsAgain runs fmt, as Taskfile.yml has it, in a copy of this
// tree: a second run skips it, as its inputs are what they were at its pass. go-task writes a
// member's checksum before the member runs, so a run killed mid-member leaves the checksum and no
// stamp; the test leaves the tree so, the checksum kept and the stamp removed, and fmt must run.
func TestAMemberKilledBeforeItsStampRunsAgain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	copyTree(t, dir)
	bin := taskBin(t)
	run := func() string {
		t.Helper()
		cmd := goTask(t, bin, "fmt")
		cmd.Dir = dir
		isolate(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("task fmt: %v:\n%s", err, out)
		}
		return string(out)
	}
	if out := run(); strings.Contains(out, upToDate) {
		t.Fatalf("the first run of fmt skipped it:\n%s", out)
	}
	if out := run(); !strings.Contains(out, upToDate) {
		t.Fatalf("the second run of fmt, its inputs unchanged, ran it:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".task", "checksum", "fmt")); err != nil {
		t.Fatalf("fmt's checksum, which a killed run leaves: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, ".task", "pass", "fmt")); err != nil {
		t.Fatalf("removing fmt's stamp: %v", err)
	}
	if out := run(); strings.Contains(out, upToDate) {
		t.Fatalf("fmt, its checksum kept and its stamp gone, as a killed run leaves it, skipped:\n%s", out)
	}
}

// notOneTag ends the line the version check says when it refuses VERSION.
const notOneTag = `", and wants one tag such as v0.1.0`

// TestTheVersionCheckRefusesAnythingButOneTag runs gorelease, changelog and release, as
// Taskfile.yml has them, in a copy of this tree, with a VERSION that is not one tag: two lines,
// either of them a good tag, or none at all. grep -x tests each line, so it passed the first two
// of them. Each task must refuse them, saying so, before it runs anything. A good tag
// must not be refused; whether changelog then passes is the notes' to say. It runs silent, so
// the line is the check's own and not the script it echoes.
//
// One summary of the three, which runs nothing, says the check is each one's first command.
// The rows of gorelease and changelog then run at once, and release's after them: a release the
// check let through would run every member, this test among them, so release runs only once the
// other two have refused each value (one go-task start after another was 4.46 s of a 10 s
// pre-push).
func TestTheVersionCheckRefusesAnythingButOneTag(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	copyTree(t, dir)
	// run runs cmd, a go-task command the caller spells out, in dir, VERSION in its environment,
	// which go-task reads as it reads VERSION=... on its command line.
	run := func(version string, cmd *exec.Cmd) (string, error) {
		cmd.Dir = dir
		isolate(cmd)
		cmd.Env = append(cmd.Env, "VERSION="+version)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	bin := taskBin(t)
	task := func(name string) *exec.Cmd {
		return goTask(t, bin, "--silent", name)
	}

	summary, err := run("v0.1.1", goTask(t, bin, "--summary", "gorelease", "changelog", "release"))
	if err != nil {
		t.Fatalf("task --summary gorelease changelog release: %v:\n%s", err, summary)
	}
	sections := map[string]string{}
	for s := range strings.SplitSeq("\n"+summary, "\ntask: ") {
		name, body, _ := strings.Cut(s, "\n")
		sections[name] = body
	}
	for _, name := range []string{"gorelease", "changelog", "release"} {
		if _, cmds, _ := strings.Cut(sections[name], "\ncommands:\n"); !strings.HasPrefix(cmds, " - Task: version\n") {
			t.Fatalf("%s's first command is not the version check, so none is run:\n%s", name, summary)
		}
	}

	bad := []string{"v0.1.1\nx", "x\nv0.1.1", ""}
	// refuses runs each bad value through each of names, all at once, and the good tag through
	// changelog with them when good is set, and waits for every one.
	refuses := func(good bool, names ...string) {
		t.Helper()
		var wg sync.WaitGroup
		for _, name := range names {
			for _, version := range bad {
				cmd := task(name)
				wg.Go(func() {
					out, err := run(version, cmd)
					if err == nil || !strings.Contains(out, notOneTag) {
						t.Errorf("task %s VERSION=%q: %v, and want it refused with %q:\n%s", name, version, err, notOneTag, out)
					}
				})
			}
		}
		if good {
			cmd := task("changelog")
			wg.Go(func() {
				if out, _ := run("v0.1.1", cmd); strings.Contains(out, notOneTag) {
					t.Errorf("task changelog VERSION=v0.1.1 refused the tag:\n%s", out)
				}
			})
		}
		wg.Wait()
	}
	refuses(true, "gorelease", "changelog")
	if t.Failed() {
		t.Fatal("the check lets a value through, so release is not run")
	}
	refuses(false, "release")
}

// TestATestMemberIsJudgedByHatchTests runs test, as Taskfile.yml has it, in a module of its own
// beside a copy of the Taskfile and hatch's modfile: one whose test fails, one
// whose test skips, one with no test, and one whose test passes, for each tier. release's run is
// test-release's: the copy's test takes TIER release, which test-release sets, as test-release is
// internal and no caller runs it alone. Each but the pass is red: the run names hatch's exit and
// the files it kept under build/, and the json kept ends in hatch's verdict, red
// for the reason the row has. Where the run goes through internal/cmd/testexec, in release and on
// Windows, it keeps an exits file too (keptExits); elsewhere pre-push runs without testexec, which
// would cost it go test's cache, and keeps the json and the stderr alone. The pass leaves build/
// with no file, the built testexec included.
//
// What it costs when this fails: a tier passes on a skipped test, or on none, as go test does;
// or a pre-push that fails in its test member leaves nothing to say why, as two of ten Windows
// tiers did, or a pass leaves a file a run a day.
func TestATestMemberIsJudgedByHatchTests(t *testing.T) {
	t.Parallel()
	bin := taskBin(t)
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	goLine, _, _ := strings.Cut(string(gomod)[strings.Index(string(gomod), "\ngo ")+1:], "\n")
	taskfile, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"pre-push": string(taskfile), "release": testAsRelease(t, string(taskfile))}
	for _, tier := range []string{"pre-push", "release"} {
		through := tier == "release" || prePushThroughTestexec
		for _, c := range []struct {
			name, file string
			// verdict is the end of hatch's last line on a red run, empty on a pass.
			verdict string
		}{
			{"a test that fails", `func TestPress(t *testing.T) { t.Fatal("the fuser is cold") }`, `: red, tests failed`},
			{"a test that skips", `func TestPress(t *testing.T) { t.Skip("the fuser is cold") }`, `: red, 1 skipped: example.invalid/kiosk.TestPress`},
			{"no test", `var _ = testing.Short`, `: red, no tests ran`},
			{"a test that passes", `func TestPress(t *testing.T) { t.Log("warm") }`, ``},
		} {
			t.Run(tier+", "+c.name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte(files[tier]), 0o600); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{
					filepath.Join("tools", "hatch.mod"),
					filepath.Join("tools", "hatch.sum"),
					filepath.Join("internal", "cmd", "testexec", "main.go"),
					filepath.Join("internal", "cmd", "testexec", "run.go"),
				} {
					if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := copyFile(name, filepath.Join(dir, name)); err != nil {
						t.Fatal(err)
					}
				}
				for name, text := range map[string]string{
					"go.mod":    "module example.invalid/kiosk\n\n" + goLine + "\n",
					"k_test.go": "package kiosk\n\nimport \"testing\"\n\n" + c.file + "\n",
				} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				cmd := goTask(t, bin, "--silent", "test")
				cmd.Dir = dir
				isolate(cmd)
				cmd.Env = quickExit(cmd.Env)
				out, err := cmd.CombinedOutput()
				kept, _ := filepath.Glob(filepath.Join(dir, "build", "*"))
				if c.verdict == "" {
					if err != nil || len(kept) != 0 {
						t.Fatalf("task test: %v, kept %q, and want a pass that keeps nothing:\n%s", err, kept, out)
					}
					return
				}
				keptRed(t, dir, out, err, c.verdict, through)
			})
		}
	}
}

// keptRed holds a red run of test in dir, err its exit and out its output. The run names hatch's
// exit and what it kept in build/: go test's json ending in a verdict that ends verdict, holding
// go test's fail where the verdict is that tests failed, and the stderr; through testexec, the
// exits file too (keptExits), and with no testexec, the json and the stderr alone.
func keptRed(t *testing.T, dir string, out []byte, err error, verdict string, through bool) {
	t.Helper()
	failed := strings.HasSuffix(verdict, ": red, tests failed")
	kept, _ := filepath.Glob(filepath.Join(dir, "build", "*"))
	named := `(?m)^test: hatch tests exited 1; go test's json and the verdict are kept in (\S+), the stderr in (\S+)\r?$`
	if through {
		named = `(?m)^test: hatch tests exited 1; go test's json and the verdict are kept in (\S+), the stderr in (\S+), each test binary's start and end in (\S+)\r?$`
	}
	m := regexp.MustCompile(named).FindSubmatch(out)
	if err == nil || m == nil {
		t.Fatalf("task test: %v, and want it red, naming what it kept:\n%s", err, out)
	}
	json, jerr := os.ReadFile(filepath.FromSlash(string(m[1])))
	last := strings.TrimRight(string(json), "\r\n")
	last = last[strings.LastIndex(last, "\n")+1:]
	if _, serr := os.Stat(filepath.FromSlash(string(m[2]))); jerr != nil || serr != nil || !strings.HasPrefix(last, "hatch tests: ") || !strings.HasSuffix(last, verdict) {
		t.Fatalf("what was kept: %v, %v, and want go test's json ending in a verdict that ends %q:\n%s", jerr, serr, verdict, json)
	}
	if failed && (!bytes.Contains(json, []byte(`"Action":"fail"`)) || !bytes.Contains(json, []byte("the fuser is cold"))) {
		t.Fatalf("the json kept holds no fail of go test's:\n%s", json)
	}
	if rel, err := filepath.Rel(filepath.Join(dir, "build"), filepath.FromSlash(string(m[1]))); err != nil || strings.Contains(rel, string(filepath.Separator)) || strings.HasPrefix(rel, "..") {
		t.Fatalf("the json is kept at %s, and want it in build/", m[1])
	}
	if !through {
		if len(kept) != 2 || !strings.HasSuffix(kept[0], ".err") || !strings.HasSuffix(kept[1], ".out") {
			t.Fatalf("a run with no testexec kept %q, and want the json and the stderr alone:\n%s", kept, out)
		}
		return
	}
	keptExits(t, string(m[3]), failed)
}

// keptExits holds the exits file a red run through internal/cmd/testexec kept at path: it holds
// the test binary's start by testexec, and, where its test failed, the binary's end with exit
// 0x00000001 and FAIL its last line.
func keptExits(t *testing.T, path string, failed bool) {
	t.Helper()
	exits, err := os.ReadFile(filepath.FromSlash(path))
	want := regexp.MustCompile(`(?m) kiosk\.test(\.exe)? pid \d+ started by testexec pid \d+ in `)
	if failed {
		want = regexp.MustCompile(`(?m) kiosk\.test(\.exe)? pid \d+ started by testexec pid \d+ in .*\n.* kiosk\.test(\.exe)? pid \d+ exited 0x00000001 after \d+\.\d+s; its last line: "FAIL"\n`)
	}
	if err != nil || !want.Match(exits) {
		t.Fatalf("the exits file kept: %v, and want it to match %s:\n%s", err, want, exits)
	}
}

// TestTheReleaseTestIsSkippedInPrePushAndRunInRelease holds the one test pre-push leaves to
// release, to its function's own name, read from the function itself:
// release-tests runs it by that name alone, and release's members include release-tests while
// pre-push's do not. The race tests' one script is the only one with a -skip, and
// TestPrePushTakesTestsFromTheCacheAndReleaseRunsEveryOne holds what each tier skips.
//
// What it costs when this fails: the test is renamed and runs in neither tier, or a -skip
// wider than its name leaves other tests to no tier.
func TestTheReleaseTestIsSkippedInPrePushAndRunInRelease(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	file := string(raw)
	pattern := "'^" + funcName(TestTheVersionCheckRefusesAnythingButOneTag) + "$'"
	for _, c := range []struct{ task, want string }{
		{"test", `-skip "$skip" ./...`},
		{"release-tests", "-run " + pattern + " ."},
		{"release-members", "\n      - release-tests\n"},
	} {
		if !strings.Contains(taskBlock(t, file, c.task), c.want) {
			t.Errorf("%s does not hold %q:\n%s", c.task, c.want, taskBlock(t, file, c.task))
		}
	}
	if n := strings.Count(file, "-skip "); n != 1 {
		t.Errorf("Taskfile.yml has %d -skip flags, and wants the race tests' script's alone", n)
	}
	if strings.Contains(taskBlock(t, file, "pre-push"), "release-tests") {
		t.Errorf("pre-push runs release-tests:\n%s", taskBlock(t, file, "pre-push"))
	}
}

// exportsRace is the line of each tier's test run that adds its race to GORACE, and the only line
// of the two tiers that names GORACE: TestPrePushTakesTestsFromTheCacheAndReleaseRunsEveryOne wants
// it right after the line that sets race, as go-task's dry run prints them.
const exportsRace = `if [ -n "$race" ]; then GORACE="${GORACE:+$GORACE }$race"; export GORACE; fi`

// TestPrePushTakesTestsFromTheCacheAndReleaseRunsEveryOne holds the flags and the skip each tier's
// race tests run with, from go-task's dry run of the two tiers, which runs
// nothing. pre-push's test has neither -count=1 nor -shuffle, either of which stops Go caching a
// package's pass, and skips release-tests' one and the one test over 1 s that guards no fail-open,
// each by its function's own name. release's test-release has -count=1 and -shuffle=on and skips
// release-tests' one alone. pre-push's test adds atexit_sleep_ms=0 to GORACE, and release's
// test-release adds nothing, so it keeps the race detector's pause at exit. The
// line right after race's in each is exportsRace, and no other line of the two tiers names GORACE,
// nor any line of Taskfile.yml outside its comments, an env: among them, which a dry run does not
// print, so race's value is what reaches the race detector.
// release's members hold test-release and not test, and pre-push's test. Each of two runs gives
// one tier's TIER and TASK, both in its environment and on its command line, where go-task
// takes a variable over its special ones, and each tier's must stay its own.
//
// What it costs when this fails: release skips a test pre-push skips, or runs a cached pass in
// place of a run, so no tier runs it again before a tag; release drops the pause at exit, and a
// race in a goroutine still running as a binary ends is red in no tier; a skipped test is
// renamed and its old name skips nothing; pre-push reruns every package and is past its budget
// again; or a caller's variable, `task release VERSION=... TIER=pre-push`, gives release
// pre-push's run.
func TestPrePushTakesTestsFromTheCacheAndReleaseRunsEveryOne(t *testing.T) {
	t.Parallel()
	type run struct{ flags, skip, race string }
	alone := funcName(TestTheVersionCheckRefusesAnythingButOneTag)
	wants := map[string]run{
		"test":         {"", "^(" + alone + "|" + funcName(TestWarmBuildsAndRunsNothing) + ")$", "atexit_sleep_ms=0"},
		"test-release": {"-count=1 -shuffle=on", "^" + alone + "$", ""},
	}
	// Each command line ends in the caller's two variables, which its environment repeats.
	for name, dry := range map[string]func(context.Context) *exec.Cmd{
		"pre-push's": func(ctx context.Context) *exec.Cmd {
			return toolTask(ctx, "--dry", "pre-push", "release", "VERSION=v0.1.1", "TIER=pre-push", "TASK=test")
		},
		"release's": func(ctx context.Context) *exec.Cmd {
			return toolTask(ctx, "--dry", "pre-push", "release", "VERSION=v0.1.1", "TIER=release", "TASK=test-release")
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cmd := dry(t.Context())
			caller := cmd.Args[len(cmd.Args)-2:]
			isolate(cmd)
			cmd.Env = append(cmd.Env, caller...)
			out, err := toolOutput(cmd)
			if err != nil {
				t.Fatalf("task --dry pre-push release, %q: %v:\n%s", caller, err, out)
			}
			runs := map[string][]run{}
			for _, m := range regexp.MustCompile(`(?m)^task: \[([a-z-]+)\] flags='([^']*)'; skip='([^']*)'; race='([^']*)'\r?\n([^\r\n]*?)\r?$`).FindAllStringSubmatch(string(out), -1) {
				runs[m[1]] = append(runs[m[1]], run{m[2], m[3], m[4]})
				if m[5] != exportsRace {
					t.Errorf("%q from the caller: task %s's line after its race is %q, want %q, which adds race and nothing else to GORACE:\n%s", caller, m[1], m[5], exportsRace, out)
				}
			}
			var naming []string
			for line := range strings.Lines(string(out)) {
				if strings.Contains(line, "GORACE") {
					naming = append(naming, strings.TrimRight(line, "\r\n"))
				}
			}
			if len(naming) != len(wants) {
				t.Errorf("%q from the caller: %d lines of the two tiers name GORACE, want %d, each tier's exportsRace:\n%s", caller, len(naming), len(wants), strings.Join(naming, "\n"))
			}
			for task, want := range wants {
				if got := runs[task]; len(got) != 1 || got[0] != want {
					t.Errorf("%q from the caller: task %s runs its tests as %+v, want one run as %+v:\n%s", caller, task, got, want, out)
				}
			}
		})
	}
	raw, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	file := string(raw)
	// A dry run prints no env:, so the file's own lines are read too: outside its comments,
	// exportsRace is the one line that names GORACE.
	var setting []string
	for line := range strings.Lines(file) {
		if line = strings.TrimSpace(line); !strings.HasPrefix(line, "#") && strings.Contains(line, "GORACE") {
			setting = append(setting, line)
		}
	}
	if len(setting) != 1 || setting[0] != exportsRace {
		t.Errorf("Taskfile.yml's lines that name GORACE, outside its comments, are %q, want exportsRace alone", setting)
	}
	if m := regexp.MustCompile(`deps: \[([^\]]*)\]`).FindStringSubmatch(taskBlock(t, file, "pre-push-members")); m == nil || !slices.Contains(strings.Split(m[1], ", "), "test") {
		t.Errorf("pre-push's members do not include test:\n%s", taskBlock(t, file, "pre-push-members"))
	}
	release := taskBlock(t, file, "release-members")
	if !strings.Contains(release, "\n      - test-release\n") || strings.Contains(release, "\n      - test\n") {
		t.Errorf("release's members hold test, or not test-release:\n%s", release)
	}
}

// TestWarmBuildsAndRunsNothing holds warm, the untimed step before a timed Tag run,
// to building and no test. go-task's dry run of it runs no task but warm, so
// no member, gate or budget step. Its go commands are these and no other: go list -export,
// which compiles and runs nothing it builds, once for each system in SYSTEMS, and once more, with
// -e, for each port go tool dist list names of a GOOS SYSTEMS leaves out, the build
// TestLintReadsEverySystemTheModuleBuildsFor wants to fail; go vet, once for
// each in BUILT, as systems runs it; go test -c, once, for the race build, which builds and
// vets the test binaries and runs none; go install, once, taskBin's build of go-task; and
// go tool -n, which builds a tool and prints where. No other task names warm, so no tier runs it and none of its time is a tier's.
//
// What it costs when this fails: the untimed step runs a test or a gate, so a host's first
// run of the tier goes untimed under another name, or a tier pays for
// the warm-up inside its budget.
func TestWarmBuildsAndRunsNothing(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	file := string(raw)
	for _, m := range regexp.MustCompile(`\n  ([a-z-]+):\n`).FindAllStringSubmatch(file, -1) {
		if m[1] == "warm" {
			continue
		}
		for line := range strings.Lines(taskBlock(t, file, m[1])) {
			if l := strings.TrimSpace(line); !strings.HasPrefix(l, "#") && strings.Contains(l, "warm") {
				t.Errorf("task %s names warm: %s", m[1], l)
			}
		}
	}
	count := func(name string) int {
		m := regexp.MustCompile(`\n  ` + name + `: (.+)\n`).FindStringSubmatch(file)
		if m == nil {
			t.Fatalf("Taskfile.yml has no var %s", name)
		}
		return len(strings.Fields(m[1]))
	}
	cmd := toolTask(t.Context(), "--dry", "warm")
	isolate(cmd)
	out, err := toolOutput(cmd)
	if err != nil {
		t.Fatalf("task --dry warm: %v:\n%s", err, out)
	}
	// Each command warm may run, whole, and how many times, by the vars it runs for.
	allowed := map[string]int{
		"go list -export -test -deps ./... > /dev/null": count("SYSTEMS"),
		// The ports SYSTEMS leaves out, one loop over go tool dist list.
		"go tool dist list | while IFS=/ read -r goos goarch; do": 1,
		"go list -e -export -deps ./... > /dev/null || exit 1":    1,
		"go vet ./...":                          count("BUILT"),
		"go test -race -c -o .task/warm/ ./...": 1,
		// taskBin's install, whose link Go caches apart from go tool's build of go-task.
		"go install -modfile=tools/task.mod github.com/go-task/task/v3/cmd/task": 1,
		`go tool -modfile="$mod" -n "$tool" > /dev/null || exit 1`:               1,
	}
	runs := map[string]int{}
	for line := range strings.Lines(string(out)) {
		if strings.HasPrefix(line, "task: [") && !strings.HasPrefix(line, "task: [warm] ") {
			t.Errorf("warm runs another task: %s", line)
		}
		for _, g := range regexp.MustCompile(`\bgo [a-z]+.*`).FindAllString(line, -1) {
			if _, ok := allowed[g]; !ok {
				t.Errorf("warm runs %q, which is none of the builds it may run", g)
			}
			runs[g]++
		}
	}
	for g, want := range allowed {
		if runs[g] != want {
			t.Errorf("warm runs %q %d times, want %d:\n%s", g, runs[g], want, out)
		}
	}
}

// funcName is the name of f, a test function of this package, read from the function itself, so a
// test renamed is a name changed here too.
func funcName(f func(*testing.T)) string {
	full := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
	return full[strings.LastIndex(full, ".")+1:]
}

// testAsRelease is file, Taskfile.yml's text, with test's TIER release, as test-release sets it:
// test-release is internal, so no caller runs it alone, and this runs its script.
func testAsRelease(t *testing.T, file string) string {
	t.Helper()
	const prePush, release, at = "\n    vars:\n      TIER: pre-push\n", "\n    vars:\n      TIER: release\n", "\n  test:\n"
	test := taskBlock(t, file, "test")
	if strings.Count(test, prePush) != 1 {
		t.Fatalf("test's TIER is not pre-push alone:\n%s", test)
	}
	head, _, _ := strings.Cut(file, at)
	return head + at + strings.Replace(test, prePush, release, 1) + file[len(head)+len(at)+len(test):]
}

// taskBlock is the task name's lines in file, Taskfile.yml's text: from "  <name>:" to the
// next task's line.
func taskBlock(t *testing.T, file, name string) string {
	t.Helper()
	_, rest, ok := strings.Cut(file, "\n  "+name+":\n")
	if !ok {
		t.Fatalf("Taskfile.yml has no task %s", name)
	}
	if i := regexp.MustCompile(`\n  [a-z-]+:\n`).FindStringIndex(rest); i != nil {
		rest = rest[:i[0]+1]
	}
	return rest
}

// copyTree copies this tree, every file but .git's, go-task's .task's (the checksums and stamps
// of this clone's runs) and build's (the json of the test member, which runs this, written as it
// copies), to dst.
func copyTree(t *testing.T, dst string) {
	t.Helper()
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.Name() == ".git" || d.Name() == ".task" || p == "build":
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case d.IsDir():
			return os.MkdirAll(filepath.Join(dst, p), 0o700)
		case !d.Type().IsRegular():
			return nil
		}
		return copyFile(p, filepath.Join(dst, p))
	})
	if err != nil {
		t.Fatalf("copying the tree to %s: %v", dst, err)
	}
}

// copyFile copies the plain file src to dst, which it makes.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { err = joinClose(err, in) }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = joinClose(err, out) }()
	_, err = io.Copy(out, in)
	return err
}

// joinClose is err, or the error closing c when err is nil.
func joinClose(err error, c io.Closer) error {
	if cerr := c.Close(); err == nil {
		return cerr
	}
	return err
}

// git runs cmd, a git command the caller spells out, in dir, isolated, and fails the test if it
// fails.
func git(t *testing.T, dir string, cmd *exec.Cmd) {
	t.Helper()
	cmd.Dir = dir
	isolate(cmd)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s in %s: %v:\n%s", strings.Join(cmd.Args, " "), dir, err, out.String())
	}
}

// isolate gives cmd the environment it would have, as exec.Cmd.Environ says it, less every GIT_
// variable, which a hook running the tests may set to this clone's repository, and with no
// global or system git config read.
func isolate(cmd *exec.Cmd) {
	var env []string
	for _, kv := range cmd.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
}

// quickExit is env with the race detector's pause at exit taken out, for the race test binary a
// row of TestATestMemberIsJudgedByHatchTests has the test member build and run. Built with -race,
// a process that exits 0 first waits GORACE's atexit_sleep_ms, a second unless set, so a goroutine
// still running may report a race; the row's one test starts no goroutine, and the second was
// most of each row's time, eight rows of a push's, as listen's quickExit says for its own
// children. The binary still exits 66 on a race it found, and the options of env's own
// GORACE are kept, the last GORACE in env being the one a process reads.
func quickExit(env []string) []string {
	opts := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GORACE="); ok {
			opts = v
		}
	}
	return append(env, "GORACE="+strings.TrimSpace(opts+" atexit_sleep_ms=0"))
}

// saysNoHistory says whether a line of out starts with noHistory: the task's own line, not the
// script that says it.
func saysNoHistory(out []byte) bool {
	for line := range strings.Lines(string(out)) {
		if strings.HasPrefix(line, noHistory) {
			return true
		}
	}
	return false
}

// TestTheBudgetHoldsEachTier runs Taskfile.yml's budget task, hatch budget's call,
// through a Taskfile that includes it, in a folder whose gate/budgets.yaml is
// this tree's with its host set to the one the test runs on, so the budgets hold here, and with
// hatch's modfile. The wrapper's end calls budget with the TIER, START and LOAD given; its
// pre-push and release are the tiers' own vars and budget calls, read from Taskfile.yml, with no
// member between, so a row within its budget is timed from a start the tier read. A tier within
// its budget passes; a pre-commit or pre-push past its budget passes when its start load is above
// 15, saying the load forgives it, and fails at 15 or with no load read; a release past its budget fails at
// any load; a start in the future, or none, or not a number, fails; on a host the
// budgets do not name, the time is printed and nothing fails. A tier run with START, LOAD and
// TIER in its environment keeps what it measured, as go-task takes a task's own var over the
// environment's. Two runs of one tier at once in one folder each keep their own verdict, and
// leave the folder as it was.
//
// What it costs when this fails: a tier past its budget passes, or one within it fails; or a run
// reads another's start, or the caller's.
func TestTheBudgetHoldsEachTier(t *testing.T) {
	t.Parallel()
	wrapper, here, elsewhere, goLine := budgetWrapper(t)
	bin := taskBin(t)
	// folder makes a folder with the wrapper, the budgets given, a go.mod and hatch's modfile.
	folder := func(t *testing.T, budgets string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "gate"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "tools"), 0o700); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{
			"Taskfile.yml":                        wrapper,
			filepath.Join("gate", "budgets.yaml"): budgets,
			"go.mod":                              "module example.invalid/budget\n\n" + goLine + "\n",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{filepath.Join("tools", "hatch.mod"), filepath.Join("tools", "hatch.sum")} {
			if err := copyFile(name, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	// run runs the wrapper with args in dir, GITHUB_ACTIONS taken out of its environment and env
	// put in.
	run := func(t *testing.T, dir string, args []string, env ...string) ([]byte, error) {
		t.Helper()
		cmd := goTask(t, bin, args...)
		cmd.Dir = dir
		isolate(cmd)
		cmd.Env = append(slices.DeleteFunc(cmd.Env, func(kv string) bool { return strings.HasPrefix(kv, "GITHUB_ACTIONS=") }), env...)
		return cmd.CombinedOutput()
	}
	// end is the wrapper's end for tier, started ago seconds back at load.
	end := func(tier string, ago int64, load string) []string {
		return []string{"end", "TIER=" + tier, "START=" + strconv.FormatInt(time.Now().Unix()-ago, 10), "LOAD=" + load}
	}
	within := func(tier string) string { return `(?m)^budget: ` + tier + `-budget took [0-9]+ s of [0-9]+ s\r?$` }
	forgiven := func(tier string) string {
		return `(?m)^budget: ` + tier + `-budget is over budget, the load forgives it: load 16 before the ` + tier + `, above the limit of 15\r?$`
	}
	over := func(tier string) string { return `(?m)^hatch budget: ` + tier + `-budget is over its budget: ` }
	twice := here + "pre-push-budget: 16\n"

	for _, c := range []struct {
		name, budgets string
		args          []string
		pass          bool
		// want is what the run must say.
		want string
	}{
		{"pre-push from its own start", here, []string{"pre-push"}, true, within("pre-push")},
		{"release from its own start", here, []string{"release"}, true, within("release")},
		{"pre-commit 1000 s ago at load 16", here, end("pre-commit", 1000, "16"), true, forgiven("pre-commit")},
		{"pre-push 1000 s ago at load 16", here, end("pre-push", 1000, "16"), true, forgiven("pre-push")},
		{"pre-commit 1000 s ago at load 15", here, end("pre-commit", 1000, "15"), false, over("pre-commit")},
		{"pre-push 1000 s ago at load 15", here, end("pre-push", 1000, "15"), false, over("pre-push")},
		{"pre-push 1000 s ago at load 1", here, end("pre-push", 1000, "1"), false, over("pre-push")},
		{"pre-commit 1000 s ago with no load", here, end("pre-commit", 1000, ""), false, over("pre-commit")},
		{"pre-push 1000 s ago with no load", here, end("pre-push", 1000, ""), false, over("pre-push")},
		{"release 1000 s ago at load 99", here, end("release", 1000, "99"), false, over("release")},
		{"release 1000 s ago with no load", here, end("release", 1000, ""), false, over("release")},
		{"pre-push 600 s from now at load 16", here, end("pre-push", -600, "16"), false, `(?m)^hatch budget: pre-push-budget: --since [0-9]+ is [0-9]+ s after now`},
		{"pre-push 600 s from now with no load", here, end("pre-push", -600, ""), false, `(?m)^hatch budget: pre-push-budget: --since [0-9]+ is [0-9]+ s after now`},
		{"pre-push with no start", here, []string{"end", "TIER=pre-push", "START=", "LOAD="}, false, ``},
		{"pre-push with a start not a number", here, []string{"end", "TIER=pre-push", "START=1795029899.5", "LOAD="}, false, ``},
		{"pre-push 1000 s ago with its budget given twice", twice, end("pre-push", 1000, ""), false, `(?m)^hatch budget: gate/budgets.yaml has no single limit for pre-push-budget\r?$`},
		{"a tier no tier is", here, end("pre-pushed", 1000, ""), false, `(?m)^budget: TIER is 'pre-pushed', and wants pre-commit, pre-push or release\r?$`},
		{"pre-push 1000 s ago on a host the budgets do not name", elsewhere, end("pre-push", 1000, ""), true, `(?m)^pre-push took [0-9]+ s; no budget for this kind of host \(`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, err := run(t, folder(t, c.budgets), c.args)
			switch {
			case c.pass && err != nil:
				t.Fatalf("task %s: %v, and want it to pass:\n%s", strings.Join(c.args, " "), err, out)
			case !c.pass && err == nil:
				t.Fatalf("task %s: passed, and want it to fail:\n%s", strings.Join(c.args, " "), out)
			case !regexp.MustCompile(c.want).Match(out):
				t.Fatalf("task %s: %v, and did not say %s:\n%s", strings.Join(c.args, " "), err, c.want, out)
			case !c.pass && bytes.Contains(out, []byte("forgives it")):
				t.Fatalf("task %s: failed, and said the load's line:\n%s", strings.Join(c.args, " "), out)
			case c.want == within("pre-push") || c.want == within("release"):
				if bytes.Contains(out, []byte("forgives it")) {
					t.Fatalf("task %s: within its budget, and said a relief's line:\n%s", strings.Join(c.args, " "), out)
				}
			}
		})
	}

	// The caller's variables, each in its environment and on its command line, where go-task takes
	// them over its special ones, as {{.TASK}}.
	t.Run("an environment that names the measures", func(t *testing.T) {
		t.Parallel()
		late := strconv.FormatInt(time.Now().Unix()+600, 10)
		out, err := runAsCaller(t, bin, folder(t, here), "pre-push", "START="+late, "LOAD=1234.5", "TIER=release", "TASK=release", "NOW=0")
		if err != nil {
			t.Fatalf("pre-push, START, LOAD and TIER from its caller: %v:\n%s", err, out)
		}
		if !regexp.MustCompile(within("pre-push")).Match(out) || bytes.Contains(out, []byte("1234.5")) {
			t.Fatalf("pre-push, START, LOAD and TIER from its caller, did not keep what it measured:\n%s", out)
		}
	})

	// A release whose caller names pre-push is held as a release: were the tier the caller's to
	// name, a release past its budget would pass at a load above 15, as a pre-push does.
	t.Run("a release whose caller names pre-push", func(t *testing.T) {
		t.Parallel()
		out, err := runAsCaller(t, bin, folder(t, here), "release", "TIER=pre-push", "TASK=pre-push")
		if err != nil {
			t.Fatalf("release, TIER and TASK naming pre-push: %v:\n%s", err, out)
		}
		if !regexp.MustCompile(within("release")).Match(out) || bytes.Contains(out, []byte("pre-push")) {
			t.Fatalf("release, TIER and TASK naming pre-push, was not held as a release:\n%s", out)
		}
	})

	// Two runs of one tier at once in one folder: neither reads the other's start or load, and
	// neither leaves a file.
	t.Run("two runs of one tier at once", func(t *testing.T) {
		t.Parallel()
		dir := folder(t, here)
		files := func() []string {
			var names []string
			if err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
				names = append(names, p)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return names
		}
		before := files()
		var wg sync.WaitGroup
		for _, c := range []struct {
			args []string
			pass bool
			want string
		}{
			{[]string{"pre-push"}, true, within("pre-push")},
			{end("pre-push", 1000, "16"), true, forgiven("pre-push")},
			{end("pre-push", 1000, "15"), false, over("pre-push")},
		} {
			wg.Go(func() {
				out, err := run(t, dir, c.args)
				if (err == nil) != c.pass || !regexp.MustCompile(c.want).Match(out) {
					t.Errorf("task %s, beside two other runs of pre-push: %v, and want it to say %s:\n%s", strings.Join(c.args, " "), err, c.want, out)
				}
			})
		}
		wg.Wait()
		if after := files(); !slices.Equal(before, after) {
			t.Errorf("two runs of pre-push left the folder %q, and want it as it was, %q", after, before)
		}
	})
}

// runAsCaller runs go-task's task in dir with each of caller's variables both on its command line
// and in its environment, GITHUB_ACTIONS taken out of it.
func runAsCaller(t *testing.T, bin, dir, task string, caller ...string) ([]byte, error) {
	t.Helper()
	cmd := goTask(t, bin, append([]string{task}, caller...)...)
	cmd.Dir = dir
	isolate(cmd)
	cmd.Env = append(slices.DeleteFunc(cmd.Env, func(kv string) bool { return strings.HasPrefix(kv, "GITHUB_ACTIONS=") }), caller...)
	return cmd.CombinedOutput()
}

// budgetWrapper is TestTheBudgetHoldsEachTier's Taskfile, which includes this one: its end calls
// budget with the TIER, START and LOAD given, and its pre-push and release are those tiers' own
// vars and budget calls, read from Taskfile.yml, with no member between. It says too
// gate/budgets.yaml with its host set to the one the test runs on, and to one no host is, and
// go.mod's go line.
func budgetWrapper(t *testing.T) (wrapper, here, elsewhere, goLine string) {
	t.Helper()
	raw, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	file := string(raw)
	budgets, err := os.ReadFile(filepath.Join("gate", "budgets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const mac = "\nhost: darwin-arm64\n"
	if strings.Count(string(budgets), mac) != 1 {
		t.Fatalf("gate/budgets.yaml has not one line %q:\n%s", strings.TrimSpace(mac), budgets)
	}
	// The host as go-task's {{OS}}-{{ARCH}} says it: the task binary is built for this host.
	goenv := exec.CommandContext(t.Context(), "go", "env", "GOHOSTOS", "GOHOSTARCH")
	host, err := goenv.Output()
	if err != nil {
		t.Fatalf("go env GOHOSTOS GOHOSTARCH: %v", err)
	}
	here = strings.Replace(string(budgets), mac, "\nhost: "+strings.Join(strings.Fields(string(host)), "-")+"\n", 1)
	elsewhere = strings.Replace(string(budgets), mac, "\nhost: plan9-mips\n", 1)
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	goLine, _, _ = strings.Cut(string(gomod)[strings.Index(string(gomod), "\ngo ")+1:], "\n")
	hatch := regexp.MustCompile(`\n  HATCH: .+\n`).FindString(file)
	if hatch == "" {
		t.Fatal("Taskfile.yml has no var HATCH")
	}
	abs, err := filepath.Abs("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	wrapper = "version: '3'\nvars:" + hatch + "includes:\n  lib:\n    taskfile: '" + filepath.ToSlash(abs) + "'\n    dir: .\ntasks:\n" +
		"  end:\n    cmds:\n      - task: lib:budget\n        vars: {TIER: '{{.TIER}}', START: '{{.START}}', LOAD: '{{.LOAD}}'}\n"
	for _, tier := range []string{"pre-push", "release"} {
		block := taskBlock(t, file, tier)
		_, vars, _ := strings.Cut(block, "\n    vars:\n")
		vars, _, _ = strings.Cut(vars, "    cmds:\n")
		call := regexp.MustCompile(`\n      - task: budget\n(        vars: .+\n)`).FindStringSubmatch(block)
		if vars == "" || call == nil {
			t.Fatalf("%s has no vars, or no budget call:\n%s", tier, block)
		}
		wrapper += "  " + tier + ":\n    vars:\n" + vars + "    cmds:\n      - task: lib:budget\n" + call[1]
	}
	return wrapper, here, elsewhere, goLine
}

// TestEachTierEndsInItsBudget holds each tier to its shape: its members, then budget, its last
// command, given the tier's own name; release checks its VERSION first.
//
// What it costs when this fails: a tier's member runs outside its time, or a tier is held to
// another's budget, and a tier past its budget passes.
func TestEachTierEndsInItsBudget(t *testing.T) {
	t.Parallel()
	// One go-task for the three summaries, which it prints in the order asked, each from
	// its "task: <name>" line: a go tool run costs about a second of CPU. It is the test's one
	// run, so go tool costs less here than taskBin's build.
	cmd := toolTask(t.Context(), "--summary", "pre-commit", "pre-push", "release")
	isolate(cmd)
	out, err := toolOutput(cmd)
	if err != nil {
		t.Fatalf("task --summary pre-commit pre-push release: %v:\n%s", err, out)
	}
	raw, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	tiers := []string{"pre-commit", "pre-push", "release"}
	summaries := strings.Split("\n"+string(out), "\ntask: ")
	if len(summaries) != len(tiers)+1 || strings.TrimSpace(summaries[0]) != "" {
		t.Fatalf("task --summary pre-commit pre-push release printed %d summaries, want %d:\n%s", len(summaries)-1, len(tiers), out)
	}
	for i, tier := range tiers {
		name, summary, _ := strings.Cut(summaries[i+1], "\n")
		if name != tier {
			t.Errorf("summary %d is of %q, want %q:\n%s", i+1, name, tier, out)
			continue
		}
		_, cmds, _ := strings.Cut(summary, "\ncommands:\n")
		if tier == "release" {
			// release checks its VERSION before anything else (TestTheVersionCheckRefusesAnythingButOneTag),
			// and then clears its stamps.
			cmds = strings.TrimPrefix(cmds, " - Task: version\n - rm -rf .task/pass\n")
		}
		if cmds != " - Task: "+tier+"-members\n - Task: budget\n" && cmds != " - Task: "+tier+"-members\n - Task: budget\n\n" {
			t.Errorf("%s is not its members and then budget:\n%q", tier, cmds)
		}
		if block := taskBlock(t, string(raw), tier); !strings.Contains(block, "\n      - task: budget\n        vars: {TIER: "+tier+", START: '{{.START}}', ") {
			t.Errorf("%s does not give budget its own name and its START:\n%s", tier, block)
		}
	}
}

// built is go-task as taskBin built it, once for the package's tests: the folder it is in, which
// TestMain removes when they end, and the build's failure, said to each test that asks.
var built struct { //nolint:gochecknoglobals // arch.ports.12: one go-task build shared by the package's tests, which no test can hand another
	once sync.Once
	dir  string
	err  error
}

// TestMain runs the package's tests, removes the folder taskBin built go-task into, as
// testkit.Main removes its own, and exits with the tests' code.
func TestMain(m *testing.M) {
	code := m.Run()
	if built.dir != "" {
		_ = os.RemoveAll(built.dir)
	}
	os.Exit(code)
}

// taskBin builds go-task from tools/task.mod, the one go tool runs, into a folder of the
// package's own, and says its path. A test that runs go-task three times or more takes it so: go
// tool resolves the tool again at each start, about a CPU-second each, most of it the system's
// (0.33 s user and 0.71 s system), where the build, its link cached, costs about two and a
// half once. It is built once for every test that asks, the first
// building it under its own context and the rest waiting: four builds at once, one a test, were
// ten CPU-seconds and the first two seconds of a push's longest package. The
// path is the package's own, not one a tool printed.
func taskBin(t *testing.T) string {
	t.Helper()
	built.once.Do(func() {
		dir, err := os.MkdirTemp("", "go-socket-task-")
		if err != nil {
			built.err = err
			return
		}
		built.dir = dir
		cmd := exec.CommandContext(t.Context(), "go", "install", "-modfile=tools/task.mod", "github.com/go-task/task/v3/cmd/task")
		cmd.Env = append(cmd.Environ(), "GOBIN="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			built.err = fmt.Errorf("go install -modfile=tools/task.mod github.com/go-task/task/v3/cmd/task into %s: %w:\n%s", dir, err, out)
		}
	})
	if built.err != nil {
		t.Fatal(built.err)
	}
	// On Windows go install writes task.exe, and exec.Command finds it from this absolute
	// path by PATHEXT, as it finds any absolute path's extension.
	return filepath.Join(built.dir, "task")
}

// goTask is the command that runs bin, the go-task taskBin built for the test, with args, under
// t's context, and with --color=false: go-task colors its output where CI is true, as on a
// release runner, and the tests read that output line by line (on a release run by hand,
// "task: gorelease" came wrapped in escape codes). The flag outranks TASK_COLOR, a taskrc and
// NO_COLOR, so no setting of the runner's turns color back on.
func goTask(t *testing.T, bin string, args ...string) *exec.Cmd {
	t.Helper()
	return exec.CommandContext(t.Context(), bin, append([]string{"--color=false"}, args...)...) //nolint:gosec // G204: the go-task the test built into its own folder (taskBin), with the test's own arguments
}

// toolTask is goTask for a test that runs go-task once and so takes it from go tool rather than
// taskBin's build: go tool -modfile=tools/task.mod task, with --color=false and args, under ctx.
// The release run by hand on v0.5.0 had TestEachTierEndsInItsBudget and
// TestPrePushTakesTestsFromTheCacheAndReleaseRunsEveryOne read no line under CI's color.
func toolTask(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "go", append([]string{"tool", "-modfile=tools/task.mod", "task", "--color=false"}, args...)...) //nolint:gosec // G204: the module's own go-task, from tools/task.mod, with the test's own arguments
}

// toolOutput runs cmd, a toolTask command, and is its combined output without the lines go tool
// prints as it fetches go-task's modules into a cold module cache, before go-task starts, such as
// "go: downloading cloud.google.com/go v0.123.0". None is go-task's, and a test that reads the
// output line by line took one for its own: warm's read "go v0.123.0" as a go command, and the
// tiers' summaries found text before the first. Any other line of go's, an error among them, stays.
func toolOutput(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.CombinedOutput()
	var kept []byte
	for line := range bytes.Lines(out) {
		if !bytes.HasPrefix(line, []byte("go: downloading ")) {
			kept = append(kept, line...)
		}
	}
	return kept, err
}

// TestTheSecondConsumerUsesEveryPackageAndRunsInPreCommit holds internal/second, a program
// on values unlike ours, to what it is for: its files import every package of the module outside
// internal/ that has code, as go list finds them, so a package added later is one the second
// consumer must use too; pre-commit's members include second, and second runs internal/second's
// tests.
//
// What it costs when this fails: a package, or a new one, is never used by a program on values
// unlike ours, or the program stops running in the tier every commit runs, and a value fixed in
// a library goes unseen until a second business meets it.
func TestTheSecondConsumerUsesEveryPackageAndRunsInPreCommit(t *testing.T) {
	t.Parallel()
	listed, err := exec.CommandContext(t.Context(), "go", "list", "-f", "{{if .GoFiles}}{{.ImportPath}}{{end}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var packages []string
	for line := range strings.Lines(string(listed)) {
		p := strings.TrimSpace(line)
		if p != "" && !strings.Contains(p+"/", "/internal/") {
			packages = append(packages, p)
		}
	}
	if len(packages) == 0 {
		t.Fatal("go list finds no package outside internal/")
	}
	files, err := filepath.Glob(filepath.Join("internal", "second", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("internal/second has no Go files: %v", err)
	}
	imported := map[string]bool{}
	for _, name := range files {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range f.Imports {
			path, err := strconv.Unquote(s.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			imported[path] = true
		}
	}
	for _, p := range packages {
		if !imported[p] {
			t.Errorf("internal/second does not import %s", p)
		}
	}
	raw, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	file := string(raw)
	if m := regexp.MustCompile(`deps: \[([^\]]*)\]`).FindStringSubmatch(taskBlock(t, file, "pre-commit-members")); m == nil || !slices.Contains(strings.Split(m[1], ", "), "second") {
		t.Errorf("pre-commit's members do not include second:\n%s", taskBlock(t, file, "pre-commit-members"))
	}
	if !strings.Contains(taskBlock(t, file, "second"), " ./internal/second/\n") {
		t.Errorf("second does not run internal/second's tests:\n%s", taskBlock(t, file, "second"))
	}
}

// TestThePromiseIndexGateRunsInPreCommit holds the gate on api/contract.txt in the commit tier:
// pre-commit's members include contract, and contract runs internal/contract's
// tests.
//
// What it costs when this fails: a commit drops a promise, or names a test that skips on a system
// it names, and nothing says so until a release.
func TestThePromiseIndexGateRunsInPreCommit(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	file := string(raw)
	if m := regexp.MustCompile(`deps: \[([^\]]*)\]`).FindStringSubmatch(taskBlock(t, file, "pre-commit-members")); m == nil || !slices.Contains(strings.Split(m[1], ", "), "contract") {
		t.Errorf("pre-commit's members do not include contract:\n%s", taskBlock(t, file, "pre-commit-members"))
	}
	if !strings.Contains(taskBlock(t, file, "contract"), " ./internal/contract/\n") {
		t.Errorf("contract does not run internal/contract's tests:\n%s", taskBlock(t, file, "contract"))
	}
}

// TestTheAPIGateRunsInPrePushAndRelease fails when api, the apidiff gate, is not
// a member of both the pre-push and the release tiers: a removed name, or a Windows-only one not
// in api/systems.txt, would otherwise reach a tag with every other test green.
func TestTheAPIGateRunsInPrePushAndRelease(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	file := string(raw)
	prePush := taskBlock(t, file, "pre-push-members")
	if m := regexp.MustCompile(`deps: \[([^\]]*)\]`).FindStringSubmatch(prePush); m == nil || !slices.Contains(strings.Split(m[1], ", "), "api") {
		t.Errorf("pre-push's members do not include api:\n%s", prePush)
	}
	if release := taskBlock(t, file, "release-members"); !strings.Contains(release, "\n      - api\n") {
		t.Errorf("release's members do not include api:\n%s", release)
	}
}
