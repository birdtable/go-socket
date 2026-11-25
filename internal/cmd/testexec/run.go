package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// exitsVar names the file testexec adds its lines to. Unset, it adds none.
const exitsVar = "GO_SOCKET_TEST_EXITS"

// waitDelay is how long testexec waits for the binary's output to close after the binary exits,
// as go test waits for a test binary's: a process the binary started may hold it open.
const waitDelay = 5 * time.Second

// lastMax is the most of the binary's last line an end line keeps.
const lastMax = 200

// forwarded are the signals testexec catches and passes to the binary. Ctrl-C and Ctrl-Break
// are os.Interrupt; Windows sends SIGTERM when its console closes; SIGQUIT is what go test sends
// on a Unix when a test runs past its -timeout, for every goroutine's stack.
func forwarded() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGQUIT, syscall.SIGTERM} }

// run starts the test binary args[0] with the rest of args, passes stdin to it, its output to
// stdout and its signals to it, records its start, each signal and its end in the file exits, and
// gives its exit code. go test gives the binary no stdin, so the null device, and testexec the
// same, which it passes on.
func run(ctx context.Context, args []string, exits string, stdin io.Reader, stdout, stderr io.Writer) int {
	return wrapper{exits: exits, wait: waitDelay}.run(ctx, args, stdin, stdout, stderr)
}

// wrapper is a run of testexec: the file it records in, and how long it waits for the binary's
// output to close after the binary exits. A binary that exits 0 with its output still open past
// wait fails, as go test fails it when it runs the binary itself ("Test I/O incomplete"):
// through testexec, go test sees testexec's output, not the binary's, so testexec must say so.
type wrapper struct {
	exits string
	wait  time.Duration
}

func (w wrapper) run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	exits, wait := w.exits, w.wait
	if len(args) == 0 {
		fmt.Fprintln(stderr, "testexec: no test binary to run")
		return 2
	}
	name := filepath.Base(args[0])
	dir, err := os.Getwd()
	if err != nil {
		dir = "(" + err.Error() + ")"
	}
	out := &tail{w: stdout}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, out, out
	cmd.WaitDelay = wait
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, forwarded()...)
	defer signal.Stop(sigs)
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		line := fmt.Sprintf("%s did not start in %s: %v", name, dir, err)
		record(exits, t0, line, stderr)
		fmt.Fprintln(stderr, "testexec:", line)
		return 2
	}
	pid := cmd.Process.Pid
	record(exits, t0, fmt.Sprintf("%s pid %d started by testexec pid %d in %s", name, pid, os.Getpid(), dir), stderr)
	done := make(chan struct{})
	passed := make(chan struct{})
	go func() {
		defer close(passed)
		for {
			select {
			case s := <-sigs:
				line := fmt.Sprintf("%s pid %d: testexec got %v and passed it on", name, pid, s)
				if err := cmd.Process.Signal(s); err != nil {
					// On Windows only a kill can be sent, and a Ctrl event reaches the binary
					// on its own console already.
					line = fmt.Sprintf("%s pid %d: testexec got %v and could not pass it on: %v", name, pid, s, err)
				}
				record(exits, time.Now(), line, stderr)
			case <-done:
				return
			}
		}
	}()
	waitErr := cmd.Wait()
	close(done)
	<-passed
	end := time.Now()
	code, how := ended(cmd.ProcessState)
	line := fmt.Sprintf("%s pid %d %s after %.3fs; its last line: %q", name, pid, how, end.Sub(t0).Seconds(), out.last())
	if err := out.err(); err != nil {
		line += fmt.Sprintf("; its output could not be passed on from then: %v", err)
	}
	if waitErr != nil && cmd.ProcessState.Success() {
		// A process the binary started held its output open past wait: go test fails this.
		line += fmt.Sprintf("; its output stayed open %v after it exited, which go test fails: %v", wait, waitErr)
		code = 1
	}
	record(exits, end, line, stderr)
	if code != 0 {
		fmt.Fprintln(stderr, "testexec:", line)
	}
	return code
}

// ended is the code testexec exits with for the binary's state, and how the binary ended. An exit
// code is given in full in hex, as Windows has it (on Windows ExitCode is never negative); a Unix
// signal by its name, and testexec then exits 1.
func ended(st *os.ProcessState) (int, string) {
	if code := st.ExitCode(); code >= 0 {
		return code, fmt.Sprintf("exited 0x%08X", code)
	}
	return 1, "ended by " + st.String()
}

// record adds the line, stamped with at, to the file exits in one write, so lines of binaries
// that run at once never mix. A line it cannot add is printed to stderr, and the run goes on.
func record(exits string, at time.Time, line string, stderr io.Writer) {
	if exits == "" {
		return
	}
	f, err := os.OpenFile(exits, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err == nil {
		_, err = f.WriteString(at.UTC().Format("2006-01-02T15:04:05.000000Z") + " " + line + "\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "testexec: recording %q: %v\n", line, err)
	}
}

// tail passes what is written to w and keeps the last line that is not empty, its first lastMax
// bytes. A write to w that fails is kept, to be recorded, and the rest is still taken, so the
// binary is never ended by a reader that went away.
type tail struct {
	w    io.Writer
	mu   sync.Mutex
	werr error  // the first write to w that failed
	cur  []byte // the line being written, up to lastMax bytes
	prev []byte // the last whole line that is not empty
}

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.werr == nil {
		if _, err := t.w.Write(p); err != nil {
			t.werr = err
		}
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		part := p
		if i >= 0 {
			part = p[:i]
		}
		if room := lastMax - len(t.cur); room > 0 {
			t.cur = append(t.cur, part[:min(room, len(part))]...)
		}
		if i < 0 {
			break
		}
		if s := bytes.TrimRight(t.cur, "\r"); len(s) > 0 {
			t.prev = append(t.prev[:0], s...)
		}
		t.cur = t.cur[:0]
		p = p[i+1:]
	}
	return n, nil
}

// last is the last line that is not empty, the one still being written if it has any bytes,
// with no \x16 before it: under go test's -json, the test binary marks its framing lines with it
// for test2json.
func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := bytes.TrimRight(t.cur, "\r")
	if len(s) == 0 {
		s = t.prev
	}
	return string(bytes.TrimPrefix(s, []byte("\x16")))
}

// err is the first write to w that failed, or nil.
func (t *tail) err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.werr
}
