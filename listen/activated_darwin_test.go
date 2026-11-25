package listen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	// launchdName is the launchd test's name, which the job it loads runs.
	launchdName = "TestActivatedTakesLaunchdSocketsByKey"
	// activatedReportEnv is the socket the launchd job's child says what it found on.
	activatedReportEnv = "GO_SOCKET_ACTIVATED_REPORT"
	// launchdWant is what the child says, in order.
	launchdWant = "inherited [false true false]\ngone true\nheld after the refusal []\nadmin true\nweb true\nready\n" +
		"again true false\nnone true\nheld at the end []\ndone\n"
)

// A job launchd loads with four sockets under the keys web, admin, extra and
// child is started, and in it Activated takes the sockets by key, as a user's
// LaunchAgent would. In order:
//
//   - child's descriptor, taken as Activated takes each, is not inherited by
//     a program started while it is open, as go-launchd marks none
//     close-on-exec; launchd marks its own already, so a copy dup made, which
//     is inherited, is not once the same marking has run on it;
//   - extra with a key the job has not is ErrNotActivated, and extra, taken
//     first, is closed with it;
//   - admin and web come back in the order asked, each named by its key and
//     local, and each is the socket at its key's path: a connection to it is
//     answered with its name;
//   - web asked again is refused as taken already, which launchd says, and
//     not as not activated;
//   - no key is fs.ErrInvalid;
//   - once its listeners are closed the child holds no named Unix socket.
//
// The child says this on a socket the test listens on, so the test waits on
// no timer.
//
// What it costs when this fails: a program launchd starts on macOS cannot
// take its socket and serves nothing, takes the wrong one, or keeps a socket
// it was refused for open, in itself and in every program it starts.
func TestActivatedTakesLaunchdSocketsByKey(t *testing.T) {
	// launchd, not this process, starts the child, so no fork holds another test's lock.
	t.Parallel()
	if os.Getenv(activatedChildEnv) == "1" {
		launchdChild(t)
		return
	}
	dir := socketDir(t)
	paths := map[string]string{}
	for _, k := range []string{"web", "admin", "extra", "child"} {
		paths[k] = socketPath(t, dir, k[:1]+".sock")
	}
	report := socketPath(t, dir, "r.sock")
	rl, err := listenOn(t, unixNet, report)
	if err != nil {
		t.Fatal(err)
	}
	defer rl.Close()
	out := filepath.Join(dir, "out.txt")
	label := "com.birdtable.go-socket.test." + strconv.Itoa(os.Getpid())
	plist := filepath.Join(t.TempDir(), "job.plist")
	if err := os.WriteFile(plist, jobPlist(label, report, out, paths), 0o600); err != nil {
		t.Fatal(err)
	}
	load(t, label, plist)
	said := func() string { b, _ := os.ReadFile(out); return string(b) }

	if err := rl.(*net.UnixListener).SetDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	rc, err := rl.Accept()
	if err != nil {
		t.Fatalf("the job's child never reported (%v); it said:\n%s", err, said())
	}
	defer rc.Close()
	if err := rc.SetDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewReader(rc)
	var heard strings.Builder
	for !strings.HasSuffix(heard.String(), "ready\n") {
		line, err := lines.ReadString('\n')
		heard.WriteString(line)
		if err != nil {
			t.Fatalf("the child stopped before its listeners were ready (%v): it reported:\n%s\nand said:\n%s", err, heard.String(), said())
		}
	}
	// Both are dialled before either is read, as the child answers in its own order.
	conns := map[string]net.Conn{}
	for _, k := range []string{"web", "admin"} {
		c, err := dial(t, unixNet, paths[k])
		if err != nil {
			t.Fatalf("launchd's socket %s at %s takes no connection: %v", k, paths[k], err)
		}
		defer c.Close()
		if err := c.SetDeadline(time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		conns[k] = c
	}
	for k, c := range conns {
		got, err := io.ReadAll(c)
		_ = c.Close()
		if err != nil || string(got) != k {
			t.Errorf("a connection to %s at %s was answered %q (%v), and want its name", k, paths[k], got, err)
		}
	}
	rest, err := io.ReadAll(lines)
	heard.Write(rest)
	if err != nil || heard.String() != launchdWant {
		t.Errorf("the child reported (%v):\n%s\nand want:\n%s\nIt said:\n%s", err, heard.String(), launchdWant, said())
	}
}

// launchdChild is the launchd test in the job's child: it asks for its
// sockets, answers a connection on each with its name, and reports each
// step on the test's socket.
func launchdChild(t *testing.T) {
	t.Helper()
	r, err := (&net.Dialer{}).DialContext(t.Context(), unixNet, os.Getenv(activatedReportEnv))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	say := func(format string, a ...any) { fmt.Fprintf(r, format+"\n", a...) }

	files, err := launchdFiles("child")
	if err != nil {
		say("child refused %v", err)
		return
	}
	inherited := make([]string, 0, len(files))
	for _, f := range files {
		inherited = append(inherited, inheritedBy(t, f.Fd()))
		// launchd marks its own already, so the marking is shown on a dup's
		// copy, which is never close-on-exec: inherited before it, not after.
		d, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		dup := os.NewFile(uintptr(d), "dup")
		inherited = append(inherited, inheritedBy(t, dup.Fd()))
		closeOnExec([]*os.File{dup})
		inherited = append(inherited, inheritedBy(t, dup.Fd()))
		if err := dup.Close(); err != nil {
			t.Error(err)
		}
	}
	say("inherited %v", inherited)
	if err := closeFiles(files); err != nil {
		t.Error(err)
	}

	_, err = Activated("extra", "gone")
	say("gone %t", errors.Is(err, ErrNotActivated))
	say("held after the refusal %q", heldSockets())
	ls, err := Activated("admin", "web")
	if err != nil {
		say("refused %v", err)
		return
	}
	for _, l := range ls {
		say("%s %t", l.Name(), l.Local())
	}
	say("ready")
	for _, l := range ls {
		answer(t, l)
	}
	_, err = Activated("web")
	say("again %t %t", errors.Is(err, syscall.EALREADY), errors.Is(err, ErrNotActivated))
	_, err = Activated()
	say("none %t", errors.Is(err, fs.ErrInvalid))
	if err := closeAll(ls); err != nil {
		t.Error(err)
	}
	say("held at the end %q", heldSockets())
	say("done")
}

// inheritedBy says whether a program this process starts now holds the
// descriptor fd: /bin/test sees its own descriptors at /dev/fd, and exits 0
// where fd is one of them and 1 where it is not. Anything else is said as
// the error.
func inheritedBy(t *testing.T, fd uintptr) string {
	t.Helper()
	err := exec.CommandContext(t.Context(), "/bin/test", "-e", "/dev/fd/"+strconv.FormatUint(uint64(fd), 10)).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return "true"
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return "false"
	default:
		return err.Error()
	}
}

// answer takes one connection on l and writes l's name to it.
func answer(t *testing.T, l *Listener) {
	t.Helper()
	if err := l.Listener.(*net.UnixListener).SetDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, l.Name()); err != nil {
		t.Error(err)
	}
}

// load loads the job in plist into the user's launchd domain, the login
// session's or, where there is none, the user's own, and boots it out when
// the test ends.
func load(t *testing.T, label, plist string) {
	t.Helper()
	uid := strconv.Itoa(os.Getuid())
	var tried []string
	for _, domain := range []string{"gui/" + uid, "user/" + uid} {
		out, err := exec.CommandContext(t.Context(), "launchctl", "bootstrap", domain, plist).CombinedOutput()
		if err == nil {
			t.Cleanup(func() {
				out, err := exec.CommandContext(context.Background(), "launchctl", "bootout", domain+"/"+label).CombinedOutput()
				if err != nil {
					t.Errorf("the job %s is still loaded (%v): %s", label, err, out)
				}
			})
			return
		}
		tried = append(tried, fmt.Sprintf("%s: %v: %s", domain, err, bytes.TrimSpace(out)))
	}
	t.Fatalf("launchd loaded the job in neither domain: %s", strings.Join(tried, "; "))
}

// jobPlist is a launchd job that runs the launchd test's child at load, with
// a socket at each path under its key, reporting on report and writing its
// output to out.
func jobPlist(label, report, out string, paths map[string]string) []byte {
	var b bytes.Buffer
	s := func(v string) {
		b.WriteString("<string>")
		_ = xml.EscapeText(&b, []byte(v))
		b.WriteString("</string>")
	}
	k := func(v string) {
		b.WriteString("<key>")
		_ = xml.EscapeText(&b, []byte(v))
		b.WriteString("</key>")
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>`)
	k("Label")
	s(label)
	k("ProgramArguments")
	b.WriteString("<array>")
	for _, a := range []string{os.Args[0], "-test.v", "-test.count=1", "-test.run=^" + launchdName + "$"} {
		s(a)
	}
	b.WriteString("</array>")
	k("EnvironmentVariables")
	b.WriteString("<dict>")
	for _, kv := range quickExit([]string{activatedChildEnv + "=1", activatedReportEnv + "=" + report}) {
		name, value, _ := strings.Cut(kv, "=")
		k(name)
		s(value)
	}
	b.WriteString("</dict>")
	k("RunAtLoad")
	b.WriteString("<true/>")
	k("Sockets")
	b.WriteString("<dict>")
	for key, p := range paths {
		k(key)
		b.WriteString("<dict>")
		k("SockPathName")
		s(p)
		b.WriteString("</dict>")
	}
	b.WriteString("</dict>")
	for _, o := range []string{"StandardOutPath", "StandardErrorPath"} {
		k(o)
		s(out)
	}
	b.WriteString("</dict></plist>\n")
	return b.Bytes()
}
