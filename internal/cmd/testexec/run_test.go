package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests run this test binary itself as the binary testexec starts, by childFlag. As with
// internal/cmd/measure, the package's TestMain is not testkit's: it is a tool the
// Taskfile runs, which imports the standard library alone and opens no socket.

// childFlag, first among a run of this binary's arguments, makes it the child: it prints its
// other arguments, one a line, then exits with the code the first names, or "kill" ends itself
// as a process is ended from outside.
const childFlag = "-testexec.child"

// TestMain is the child when the binary is run with childFlag, and runs the tests otherwise.
func TestMain(m *testing.M) {
	if len(os.Args) < 3 || os.Args[1] != childFlag {
		os.Exit(m.Run())
	}
	for _, a := range os.Args[3:] {
		fmt.Println(a)
	}
	switch os.Args[2] {
	case "linger":
		// The grandchild: it holds the output it was given, the child's, until its stdin ends.
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			os.Exit(9)
		}
		os.Exit(0)
	case "hold":
		// It starts the grandchild on its own stdin and output, and exits 0 while that runs on.
		// This TestMain is the child process's main, where its context starts.
		g := exec.CommandContext(context.Background(), os.Args[0], childFlag, "linger") //nolint:gosec // G204, G702: the test binary itself, with the test's own arguments
		g.Stdin, g.Stdout, g.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := g.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "the child could not start its own:", err)
			os.Exit(9)
		}
		os.Exit(0)
	}
	if os.Args[2] == "kill" {
		p, err := os.FindProcess(os.Getpid())
		if err == nil {
			err = p.Kill()
		}
		fmt.Fprintln(os.Stderr, "the child could not end itself:", err)
		os.Exit(9)
	}
	code, err := strconv.Atoi(os.Args[2])
	if err != nil {
		os.Exit(9)
	}
	os.Exit(code)
}

// child runs testexec on this binary as the child, with the exits file in a folder of the test's,
// and gives testexec's code, its stdout, its stderr and the exits file's lines.
func child(t *testing.T, args ...string) (int, string, string, []string) {
	t.Helper()
	exits := filepath.Join(t.TempDir(), "exits")
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), append([]string{os.Args[0], childFlag}, args...), exits, nil, &stdout, &stderr)
	raw, err := os.ReadFile(exits)
	if err != nil {
		t.Fatalf("the exits file: %v", err)
	}
	return code, stdout.String(), stderr.String(), strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

var (
	startLine = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{6}Z (\S+) pid (\d+) started by testexec pid (\d+) in (.+)$`)
	endLine   = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{6}Z (\S+) pid (\d+) (.+) after \d+\.\d{3}s; its last line: (".*")$`)
)

// lines checks the exits file holds a start line and an end line for one binary, the same binary
// and pid, and gives how the end line says it ended and its last line.
func lines(t *testing.T, got []string) (string, string) {
	t.Helper()
	if len(got) != 2 {
		t.Fatalf("the exits file holds %d lines, and want a start and an end:\n%s", len(got), strings.Join(got, "\n"))
	}
	s, e := startLine.FindStringSubmatch(got[0]), endLine.FindStringSubmatch(got[1])
	if s == nil || e == nil {
		t.Fatalf("the exits file's lines are not a start and an end:\n%s", strings.Join(got, "\n"))
	}
	wd, _ := os.Getwd()
	if s[1] != filepath.Base(os.Args[0]) || s[1] != e[1] || s[2] != e[2] || s[3] != strconv.Itoa(os.Getpid()) || s[4] != wd {
		t.Fatalf("the start and end name different binaries, or not this one in this folder:\n%s", strings.Join(got, "\n"))
	}
	last, err := strconv.Unquote(e[4])
	if err != nil {
		t.Fatal(err)
	}
	return e[3], last
}

// What it costs when this fails: a test binary's pass reads as a failure in go test, or the tier
// keeps no line to say a binary ran.
func TestABinaryThatPassesIsRecordedAndSaysNothingMore(t *testing.T) {
	t.Parallel()
	code, stdout, stderr, got := child(t, "0", "ok\tkiosk", "PASS")
	if code != 0 || stdout != "ok\tkiosk\nPASS\n" || stderr != "" {
		t.Fatalf("code %d, stdout %q, stderr %q, and want 0, the child's lines, and nothing", code, stdout, stderr)
	}
	if how, last := lines(t, got); how != "exited 0x00000000" || last != "PASS" {
		t.Fatalf("the end line says %q, last %q, and want exited 0x00000000, last PASS", how, last)
	}
}

// What it costs when this fails: a failed test binary's code is lost on its way to go test, or
// the tier keeps no exit code beside the binary's last line to tell a failure from a kill.
func TestABinaryThatFailsGivesItsCodeInFullAndItsLastLine(t *testing.T) {
	t.Parallel()
	code, stdout, stderr, got := child(t, "3", "--- FAIL: TestPress", "FAIL", "")
	if code != 3 || stdout != "--- FAIL: TestPress\nFAIL\n\n" {
		t.Fatalf("code %d, stdout %q, and want 3 and the child's lines", code, stdout)
	}
	how, last := lines(t, got)
	if how != "exited 0x00000003" || last != "FAIL" {
		t.Fatalf("the end line says %q, last %q, and want exited 0x00000003, last FAIL, the empty line passed over", how, last)
	}
	if want := "testexec: " + got[1][strings.Index(got[1], " ")+1:] + "\n"; stderr != want {
		t.Fatalf("stderr %q, and want the end line %q", stderr, want)
	}
}

// What it costs when this fails: a binary ended from outside, as on the Windows tiers
// that failed with no test line, reads in the exits file as one that failed, or is not recorded.
func TestABinaryEndedFromOutsideIsToldFromOneThatFailed(t *testing.T) {
	t.Parallel()
	code, _, stderr, got := child(t, "kill", "=== RUN   TestPress")
	how, last := lines(t, got)
	if code != 1 || how != killedHow || last != "=== RUN   TestPress" || !strings.HasPrefix(stderr, "testexec: ") {
		t.Fatalf("code %d, end %q, last %q, stderr %q, and want 1, %q, the line it stopped on, and the end line", code, how, last, stderr, killedHow)
	}
}

// What it costs when this fails: a binary that cannot start passes, or leaves no line.
func TestABinaryThatCannotStartIsRecordedAndFails(t *testing.T) {
	t.Parallel()
	exits := filepath.Join(t.TempDir(), "exits")
	var stdout, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "kiosk.test")
	if code := run(t.Context(), []string{missing}, exits, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("a binary that is not there: code %d, and want 2", code)
	}
	raw, err := os.ReadFile(exits)
	if err != nil || !strings.Contains(string(raw), "kiosk.test did not start in ") || !strings.Contains(stderr.String(), "kiosk.test did not start in ") {
		t.Fatalf("the exits file %q (%v) and stderr %q, and want both to say it did not start", raw, err, stderr.String())
	}
	if code := run(t.Context(), nil, exits, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("no binary at all: code %d, and want 2", code)
	}
}

// What it costs when this fails: with no exits file named, testexec fails the binary it runs,
// or says so on every run.
func TestWithNoExitsFileTheBinaryRunsAsBefore(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{os.Args[0], childFlag, "0", "PASS"}, "", nil, &stdout, &stderr); code != 0 || stdout.String() != "PASS\n" || stderr.Len() != 0 {
		t.Fatalf("code %d, stdout %q, stderr %q, and want 0, PASS and nothing", code, stdout.String(), stderr.String())
	}
	if code := run(t.Context(), []string{os.Args[0], childFlag, "4", "FAIL"}, "", nil, &stdout, &stderr); code != 4 || !strings.Contains(stderr.String(), "exited 0x00000004") {
		t.Fatalf("code %d, stderr %q, and want 4 and the end line", code, stderr.String())
	}
}

// What it costs when this fails: the last line names the wrong line, or a long one grows the
// exits file without end.
func TestTheLastLineIsTheLastThatIsNotEmptyAndShort(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", lastMax+50)
	for _, c := range []struct {
		name   string
		writes []string
		want   string
	}{
		{"none", nil, ""},
		{"one, unended", []string{"=== RUN   Test"}, "=== RUN   Test"},
		{"crlf, then empty lines", []string{"PASS\r\n", "\r\n", "\n"}, "PASS"},
		{"a line in pieces", []string{"--- FA", "IL: T", "\nFAIL\n"}, "FAIL"},
		{"long", []string{long + "\n"}, long[:lastMax]},
		{"long, in pieces", []string{long[:150], long[150:], "\n"}, long[:lastMax]},
		{"an unended line after a whole one", []string{"a\nb"}, "b"},
		{"test2json's mark", []string{"\x16--- FAIL: TestPress (0.00s)\n\x16FAIL\n"}, "FAIL"},
	} {
		var w bytes.Buffer
		tl := &tail{w: &w}
		for _, s := range c.writes {
			if n, err := tl.Write([]byte(s)); n != len(s) || err != nil {
				t.Fatalf("%s: Write(%q) = %d, %v", c.name, s, n, err)
			}
		}
		if got := tl.last(); got != c.want || w.String() != strings.Join(c.writes, "") || tl.err() != nil {
			t.Errorf("%s: last %q, passed %q, err %v, and want %q and every byte", c.name, got, w.String(), tl.err(), c.want)
		}
	}
}

// What it costs when this fails: a test binary whose own child holds its output open past the
// wait, which go test fails ("Test I/O incomplete"), passes through testexec, and the tier with it.
func TestABinaryWhoseOutputOutlivesItFailsAsGoTestFailsIt(t *testing.T) {
	t.Parallel()
	exits := filepath.Join(t.TempDir(), "exits")
	// The grandchild holds the binary's output until the test closes w, its stdin's other end.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := errors.Join(r.Close(), w.Close()); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
	var stdout, stderr bytes.Buffer
	code := wrapper{exits: exits, wait: 100 * time.Millisecond}.run(t.Context(), []string{os.Args[0], childFlag, "hold", "PASS"}, r, &stdout, &stderr)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exits)
	if err != nil {
		t.Fatal(err)
	}
	want := "; its output stayed open 100ms after it exited, which go test fails: " + exec.ErrWaitDelay.Error()
	got, cut := strings.CutSuffix(strings.TrimSuffix(string(raw), "\n"), want)
	how, last := lines(t, strings.Split(got, "\n"))
	if code != 1 || !cut || how != "exited 0x00000000" || last != "PASS" || !strings.Contains(stderr.String(), want) {
		t.Fatalf("code %d, end %q, last %q, exits %q, stderr %q, and want 1, exited 0x00000000, PASS, and %q on the end line and stderr", code, how, last, raw, stderr.String(), want)
	}
}

// broken is a reader gone: every write fails.
type broken struct{}

func (broken) Write([]byte) (int, error) { return 0, os.ErrClosed }

// What it costs when this fails: a binary whose go test went away is ended by the write that
// fails, and nothing records that its output stopped reaching go test.
func TestAReaderGoneEndsNothingAndIsRecorded(t *testing.T) {
	t.Parallel()
	tl := &tail{w: broken{}}
	for _, s := range []string{"=== RUN   TestPress\n", "PASS\n"} {
		if n, err := tl.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write(%q) to a reader gone = %d, %v, and want it taken", s, n, err)
		}
	}
	if tl.last() != "PASS" || !errors.Is(tl.err(), os.ErrClosed) {
		t.Fatalf("last %q, err %v, and want PASS and the write that failed", tl.last(), tl.err())
	}
	exits := filepath.Join(t.TempDir(), "exits")
	var stderr bytes.Buffer
	if code := run(t.Context(), []string{os.Args[0], childFlag, "0", "PASS"}, exits, nil, broken{}, &stderr); code != 0 {
		t.Fatalf("a binary whose output reaches no one: code %d, and want its own 0", code)
	}
	raw, err := os.ReadFile(exits)
	if err != nil || !strings.Contains(string(raw), `exited 0x00000000 after `) || !strings.Contains(string(raw), "; its output could not be passed on from then: "+os.ErrClosed.Error()) {
		t.Fatalf("the exits file %q (%v), and want the end and the write that failed", raw, err)
	}
}
