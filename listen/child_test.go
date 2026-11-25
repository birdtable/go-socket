package listen

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// childListen, set to 1, makes the test binary a program that listens at childAt the folder it
// is started in: the child of the tests that need a second process. It says "listening",
// "held: <why>" or "refused: <why>" on its first line of standard error, listens until its
// standard input ends, and then closes.
const childListen = "GO_SOCKET_LISTEN_TEST_CHILD"

// runChild is the listening child when the test that started it said so, and returns at once
// in every other test process.
func runChild() {
	if os.Getenv(childListen) != "1" {
		return
	}
	wd, err := os.Getwd()
	if err != nil {
		println("refused:", err.Error())
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	l, err := Listen(ctx, childAt(wd))
	cancel()
	switch {
	case errors.Is(err, ErrHeld):
		println("held:", err.Error())
		os.Exit(3)
	case err != nil:
		println("refused:", err.Error())
		os.Exit(1)
	}
	println("listening")
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := l.Close(); err != nil {
		println("close:", err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}

// quickExit is env with the race detector's pause at exit taken out, for a child these tests
// start from their own binary. Built with -race, a process that exits 0 first waits GORACE's
// atexit_sleep_ms, a second unless set, so a goroutine still running may report a race; then
// it exits 66 if it found one. The racing test's hundred rounds each wait for a child that
// exits 0, which made it a hundred seconds of a push. The child still exits 66
// on a race it found, which fails the test that started it; the caller's own GORACE options
// are kept, and without -race nothing reads GORACE.
func quickExit(env []string) []string {
	return append(env, "GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
}

// child is a listening child a test started.
type child struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	said  *bufio.Reader
}

// startChild starts the test binary as a program that listens at childAt dir, killed when
// the test ends.
func startChild(t *testing.T, dir string) *child {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
	cmd.Env = append(quickExit(os.Environ()), childListen+"=1")
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: cmd, stdin: stdin, said: bufio.NewReader(stderr)}
	t.Cleanup(func() { _ = c.cmd.Process.Kill(); _ = c.cmd.Wait() })
	return c
}

// first is what the child said first: "listening", or why not.
func (c *child) first(t *testing.T) string {
	t.Helper()
	line, err := c.said.ReadString('\n')
	if err != nil && line == "" {
		t.Fatalf("the child said nothing: %v", err)
	}
	return strings.TrimSpace(line)
}

// stop ends the child's standard input, so a listening child closes, and gives its exit code.
func (c *child) stop(t *testing.T) int {
	t.Helper()
	_ = c.stdin.Close()
	_, _ = io.Copy(io.Discard, c.said)
	err := c.cmd.Wait()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("the child: %v", err)
	}
	return c.cmd.ProcessState.ExitCode()
}
