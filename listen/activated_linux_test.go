package listen

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	// activatedName is the activation test's name, which its child is started to run.
	activatedName = "TestActivatedKeepsTheOrderAndTheNames"
	// namedName is the test of names asked for, which its child is started to run.
	namedName = "TestActivatedTakesTheNamedInTheirOrderAndClosesTheRest"
	// activatedNamesEnv is the names the child asks for, joined by commas.
	activatedNamesEnv = "GO_SOCKET_ACTIVATED_NAMES"
)

// Two named sockets passed as systemd passes them come back in the order
// passed, each with its name, its path and as local, and the variables that
// passed them are cleared, so a program the child starts does not take them
// too. They are passed the way systemd passes them, by a shell that sets the
// variables before the exec, and, where the system has it, by systemd's own
// tool for starting a program by its sockets.
//
// What it costs when this fails: a program with a public and an admin socket
// serves the admin handler on the public one, or a child it starts takes its
// sockets.
func TestActivatedKeepsTheOrderAndTheNames(t *testing.T) {
	// serial: it starts child processes, and a fork while another test holds a lock file open
	// holds that lock until the exec, so a parallel test would find its lock taken.
	if os.Getenv(activatedChildEnv) == "1" {
		activatedChild(t)
		return
	}
	dir := socketDir(t)
	web, admin := socketPath(t, dir, "w.sock"), socketPath(t, dir, "a.sock")
	want := "web " + web + " true\nadmin " + admin + " true\n"
	out, err := underShell(t, activatedName, "web:admin", "", passed(t, web, admin))
	if err != nil || !strings.Contains(out, want) || strings.Contains(out, "held ") || !strings.Contains(out, "--- PASS: "+activatedName) {
		t.Errorf("passed by a shell, the child said (%v):\n%s\nand want, in order, and none held after:\n%s", err, out, want)
	}

	tool, err := exec.LookPath("systemd-socket-activate")
	if err != nil {
		t.Logf("systemd's tool is not here (%v): the sockets were passed by the shell alone", err)
		return
	}
	underTool(t, tool, filepath.Join(dir, "tool"))
}

// Of three sockets passed, the two asked for by name come back in the order
// asked, not the order passed, and the third is closed; a name nothing was
// passed under is ErrNotActivated, and then every socket passed is closed.
// The child says each named Unix socket it still holds once its listeners
// are closed, and none may be.
//
// What it costs when this fails: a program's admin handler serves its public
// socket, or a socket it did not ask for stays open in it, and in every
// program it starts, taking connections nobody answers.
func TestActivatedTakesTheNamedInTheirOrderAndClosesTheRest(t *testing.T) {
	// serial: it starts child processes, and a fork while another test holds a lock file open
	// holds that lock until the exec, so a parallel test would find its lock taken.
	if os.Getenv(activatedChildEnv) == "1" {
		activatedChild(t)
		return
	}
	dir := socketDir(t)
	web, admin, spare := socketPath(t, dir, "w.sock"), socketPath(t, dir, "a.sock"), socketPath(t, dir, "s.sock")
	files := passed(t, web, admin, spare)
	for _, c := range []struct{ names, want string }{
		{"admin,web", "admin " + admin + " true\nweb " + web + " true\n"},
		{"web,gone", "refused true "},
	} {
		out, err := underShell(t, namedName, "web:admin:spare", c.names, files)
		if err != nil || !strings.Contains(out, c.want) || strings.Contains(out, "held ") || !strings.Contains(out, "--- PASS: "+namedName) {
			t.Errorf("asked for %s, the child said (%v):\n%s\nand want, and none held after:\n%s", c.names, err, out, c.want)
		}
	}
}

// passed is a listening socket's descriptor at each path, as systemd would
// hold them, each closed when the test ends.
func passed(t *testing.T, paths ...string) []*os.File {
	t.Helper()
	files := make([]*os.File, 0, len(paths))
	for _, p := range paths {
		l, err := listenOn(t, unixNet, p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		f, err := l.(*net.UnixListener).File()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		files = append(files, f)
	}
	return files
}

// underShell runs the test named test in a child passed files as systemd
// passes them, under fdnames, asking for names, and is what the child said.
// LISTEN_PID must be the child's own, which only the child's shell knows
// before the exec, as systemd sets it.
func underShell(t *testing.T, test, fdnames, names string, files []*os.File) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c",
		`LISTEN_PID=$$ LISTEN_FDS=$2 LISTEN_FDNAMES=$3 exec "$0" -test.v -test.count=1 -test.run="^$1\$"`,
		os.Args[0], test, strconv.Itoa(len(files)), fdnames)
	cmd.Env = append(quickExit(os.Environ()), activatedChildEnv+"=1", activatedNamesEnv+"="+names)
	cmd.ExtraFiles = files
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// underTool passes two named sockets in folder by systemd's own tool, which
// binds them, waits for a connection, and then starts the child.
func underTool(t *testing.T, tool, folder string) {
	t.Helper()
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	web, admin := socketPath(t, folder, "w.sock"), socketPath(t, folder, "a.sock")
	want := "web " + web + " true\nadmin " + admin + " true\n"
	cmd := exec.CommandContext(t.Context(), tool, "-l", web, "-l", admin, "--fdname=web:admin",
		"-E", activatedChildEnv+"=1", "-E", "PATH",
		os.Args[0], "-test.v", "-test.count=1", "-test.run=^"+activatedName+"$")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	said := bufio.NewReader(stderr)
	var heard strings.Builder
	for listening := 0; listening < 2; {
		line, err := said.ReadString('\n')
		heard.WriteString(line)
		if err != nil {
			_ = cmd.Wait()
			t.Fatalf("the tool stopped before it listened on both (%v):\n%s", err, heard.String())
		}
		if strings.HasPrefix(line, "Listening on ") {
			listening++
		}
	}
	c, err := dial(t, unixNet, web)
	if err != nil {
		t.Fatalf("the tool's socket does not take a connection: %v", err)
	}
	defer c.Close()
	rest, _ := io.ReadAll(said)
	heard.Write(rest)
	err = cmd.Wait()
	if err != nil || !strings.Contains(heard.String(), want) || strings.Contains(heard.String(), "held ") || !strings.Contains(stdout.String(), "--- PASS: "+activatedName) {
		t.Errorf("passed by systemd's tool, the child said (%v):\n%s%s\nand want, in order:\n%s", err, heard.String(), stdout.String(), want)
	}
}

// activatedChild is the activation test in the child it starts: it takes
// what was passed, under the names activatedNamesEnv lists if it is set, and
// says each listener's name, address and whether it is local, one to a line,
// in order, or that it was refused and whether as not activated; then, once
// every listener is closed, each named Unix socket it still holds.
func activatedChild(t *testing.T) {
	t.Helper()
	var names []string
	if v := os.Getenv(activatedNamesEnv); v != "" {
		names = strings.Split(v, ",")
	}
	ls, err := Activated(names...)
	if err != nil {
		println("refused", errors.Is(err, ErrNotActivated), err.Error())
	}
	for _, v := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		if os.Getenv(v) != "" {
			t.Errorf("%s is still set, and a program this one starts would take its sockets", v)
		}
	}
	for _, l := range ls {
		println(l.Name(), l.Addr().String(), l.Local())
	}
	if err := closeAll(ls); err != nil {
		t.Error(err)
	}
	for _, h := range heldSockets() {
		println("held", h)
	}
}
