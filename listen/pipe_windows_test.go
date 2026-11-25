package listen

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	winio "github.com/Microsoft/go-winio"
)

// local is an address of the test's own for a listener on this machine: a
// pipe's name no other test, nor run, has.
func local(t *testing.T) string {
	t.Helper()
	return pipePrefix + "go-socket-listen-" + rand.Text()
}

// dialLocal connects to the local listener at addr, a pipe.
func dialLocal(ctx context.Context, addr string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, addr)
}

// childAt is where the listening child listens, started in dir: a pipe named
// for dir, which is the child's own folder, so no other test's child takes it
// and nothing from outside reaches Listen.
func childAt(dir string) string { return pipePrefix + "go-socket-child-" + filepath.Base(dir) }

// holding accepts on l and keeps each connection open until the test ends,
// so a client of the pipe reads it while its server end is still open.
func holding(t *testing.T, l net.Listener) {
	t.Helper()
	conns := make(chan net.Conn, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			select {
			case conns <- c:
			default:
				_ = c.Close()
			}
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		<-done
		close(conns)
		for c := range conns {
			_ = c.Close()
		}
	})
}

// fileRight is what Windows keeps for a right an access list names that a
// pipe is opened with, as a file: a generic right is kept as the file rights
// it maps to, so GA reads back as FA and GRGW as FRFW.
func fileRight(two string) (uint32, bool) {
	switch two {
	case "GA", "FA":
		return 0x1f01ff, true
	case "GR", "FR":
		return 0x120089, true
	case "GW", "FW":
		return 0x120116, true
	case "GX", "FX":
		return 0x1200a0, true
	}
	return 0, false
}

// rights is an entry's rights, as SDDL writes them, as the one number they
// grant: two letters a right, or a hex number.
func rights(s string) (uint32, error) {
	if strings.HasPrefix(s, "0x") {
		n, err := strconv.ParseUint(s[2:], 16, 32)
		return uint32(n), err
	}
	if len(s)%2 != 0 || s == "" {
		return 0, fmt.Errorf("the rights %q are not two letters each", s)
	}
	var all uint32
	for i := 0; i < len(s); i += 2 {
		r, ok := fileRight(s[i : i+2])
		if !ok {
			return 0, fmt.Errorf("the right %q in %q is not one a pipe's list is read with here", s[i:i+2], s)
		}
		all |= r
	}
	return all, nil
}

// readBack is the account PowerShell runs as, by its SID, and the owner
// ("O:" and a SID) and access list ("D:" on) of the pipe at name, as SDDL
// writes them, read with the system's own tools and not with listen's: .NET's
// NamedPipeClientStream dials the pipe, and GetAccessControl reads its
// security descriptor from that handle, so name must be accepting, and
// holding what it accepts. Only standard output is read: Windows PowerShell
// writes progress to standard error as CLIXML on an account's first run, and
// progress is turned off besides. Get-Acl
// cannot read a pipe: Windows PowerShell's file provider says \\.\pipe\ and a
// name does not exist.
func readBack(t *testing.T, name string) (me, owner, dacl string) {
	t.Helper()
	const script = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$pipe = New-Object System.IO.Pipes.NamedPipeClientStream('.', $env:GO_SOCKET_PIPE, [System.IO.Pipes.PipeDirection]::InOut)
try {
  $pipe.Connect(10000)
  $acl = $pipe.GetAccessControl()
  $acl.GetSecurityDescriptorSddlForm('Owner')
  $acl.GetSecurityDescriptorSddlForm('Access')
} finally {
  $pipe.Dispose()
}`
	short, ok := strings.CutPrefix(name, pipePrefix)
	if !ok {
		t.Fatalf("%s is not a pipe's name", name)
	}
	cmd := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "GO_SOCKET_PIPE="+short)
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			stderr = exit.Stderr
		}
		t.Fatalf("the pipe's access list could not be read (%v):\n%s\n%s", err, out, stderr)
	}
	lines := strings.Fields(string(out))
	if len(lines) != 3 {
		t.Fatalf("PowerShell said %d lines, and want the account, the owner and the list:\n%s", len(lines), out)
	}
	return lines[0], lines[1], lines[2]
}

// fullSID is who, a SID as SDDL writes it, as the full S-1-... string. SDDL writes some SIDs by
// an alias: SY for SYSTEM, and LA for the machine's built-in Administrator, which GitHub's
// Windows runners run as. Windows' own ConvertStringSidToSid reads either, and an alias for a
// group, BA or WD, gives that group's SID, never the account's.
//
// What it costs when this fails: the account's own entry, written LA, reads as a stranger's, and
// the test is red on a pipe whose list is right; or the test compares aliases, and passes a list
// a group's alias is in.
func fullSID(t *testing.T, who string) string {
	t.Helper()
	sid, err := syscall.StringToSid(who)
	if err != nil {
		t.Fatalf("%q is neither a SID nor an alias Windows reads: %v", who, err)
	}
	s, err := sid.String()
	if err != nil {
		t.Fatalf("the SID %q has no string: %v", who, err)
	}
	return s
}

// entry is one access list entry, as SDDL writes it.
type entry struct {
	kind, flags string
	rights      uint32
	who         string
}

// entries is the entries of dacl, and its flags: what is between "D:" and
// the first entry.
func entries(dacl string) (flags string, es []entry, err error) {
	rest, ok := strings.CutPrefix(dacl, "D:")
	if !ok {
		return "", nil, fmt.Errorf("%q is not an access list", dacl)
	}
	flags, rest, _ = strings.Cut(rest, "(")
	rest = "(" + rest
	for rest != "" {
		var e string
		if !strings.HasPrefix(rest, "(") {
			return "", nil, fmt.Errorf("%q: %q is not an entry", dacl, rest)
		}
		e, rest, ok = strings.Cut(rest[1:], ")")
		if !ok {
			return "", nil, fmt.Errorf("%q: an entry has no end", dacl)
		}
		f := strings.Split(e, ";")
		if len(f) != 6 {
			return "", nil, fmt.Errorf("%q: the entry %q has %d fields, and want 6", dacl, e, len(f))
		}
		r, err := rights(f[2])
		if err != nil {
			return "", nil, err
		}
		es = append(es, entry{kind: f[0], flags: f[1], rights: r, who: f[5]})
	}
	return flags, es, nil
}

// A pipe, read back once Listen made it, is owned by the account the program
// runs as, by the SID Windows says it is, and its access list is protected
// (P), so nothing is inherited into it, has no entry marked inherited, and
// holds two entries and nothing else, each one that allows: SYSTEM with every
// right, and the account with read and write (FRFW, 0x12019f), which on a
// pipe holds making the next server end. Read back through .NET's pipe
// client, not with listen's own code, and the account from Windows' own identity, not from
// os/user.
//
// What it costs when this fails: the pipe is opened by an account it does not
// name, Administrators or Everyone, or by every account its default list lets
// in, and that account talks to the program as the program's own caller; or
// Administrators own it, and may rewrite its list.
func TestAPipesAccessListIsTheAccountAndSystemAlone(t *testing.T) {
	t.Parallel()
	name := local(t)
	l, err := Listen(soon(t), name)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	holding(t, l)
	me, owner, dacl := readBack(t, name)
	if who, ok := strings.CutPrefix(owner, "O:"); !ok || fullSID(t, who) != me {
		t.Errorf("the pipe's owner is %s, and want the account, O:%s", owner, me)
	}
	flags, es, err := entries(dacl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flags, "P") || strings.Contains(flags, "AI") {
		t.Errorf("the pipe's list %s is not protected, and want P", dacl)
	}
	for i := range es {
		es[i].who = fullSID(t, es[i].who)
	}
	want := []entry{{kind: "A", rights: 0x1f01ff, who: fullSID(t, "SY")}, {kind: "A", rights: 0x12019f, who: me}}
	key := func(e entry) string { return e.who }
	slices.SortFunc(es, func(a, b entry) int { return strings.Compare(key(a), key(b)) })
	slices.SortFunc(want, func(a, b entry) int { return strings.Compare(key(a), key(b)) })
	if !slices.Equal(es, want) {
		t.Errorf("the pipe's list is %s, %+v, and want %+v alone", dacl, es, want)
	}
}

// The pipe's access list is the account SID os/user gives, and it is checked
// as Windows writes a SID back: anything that is not a SID, an SDDL alias, or
// a SID with more after it is refused, so nothing but the one account reaches
// the list.
//
// What it costs when this fails: an account name like WD, or a SID with
// ")(A;;GA;;;WD" after it, opens the pipe to Everyone.
func TestThePipesAccountIsASIDAlone(t *testing.T) {
	t.Parallel()
	for _, sid := range []string{"", "WD", "BA", "S-1-5-21-1-2-3-1001)(A;;GA;;;WD", "S-1-5-21-1-2-3-1001 ", "s-1-5-21-1-2-3-1001", "S-1-5-021-1-2-3-1001"} {
		if got, err := pipeSDDL(sid); err == nil {
			t.Errorf("the account %q made the list %q, and want it refused", sid, got)
		}
	}
	const sid = "S-1-5-21-1-2-3-1001"
	got, err := pipeSDDL(sid)
	if err != nil || got != "O:"+sid+"D:P(A;;GA;;;SY)(A;;FRFW;;;"+sid+")" {
		t.Errorf("the account %s: %q (%v), and want it the owner, and SYSTEM and it alone in the list, protected", sid, got, err)
	}
}

// SocketGroup, SocketMode and ReplaceEvenIfBusy, which mean something for a
// Unix socket alone, are each refused on Windows with an error that names it
// and wraps errors.ErrUnsupported, and no pipe is made: the name is free
// after. SocketMode is refused at every mode, 0600 too; the default, with no
// option, listens.
//
// What it costs when this fails: a program written for Unix asks for 0660 and
// a gate's group, listens on Windows with the access list alone, and believes
// the group reaches it, or believes a stale socket rule protects it.
func TestUnixSocketOptionsAreUnsupportedOnWindows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opt  ListenOption
	}{
		{"SocketGroup", SocketGroup("gate")},
		{"SocketMode", SocketMode(0o600)},
		{"SocketMode", SocketMode(0o660)},
		{"SocketMode", SocketMode(0o777)},
		{"ReplaceEvenIfBusy", ReplaceEvenIfBusy()},
	}
	ran := 0
	for _, tc := range cases {
		ran++
		name := local(t)
		l, err := Listen(soon(t), name, tc.opt)
		if err == nil {
			_ = l.Close()
		}
		if !errors.Is(err, errors.ErrUnsupported) || !strings.Contains(fmt.Sprint(err), tc.name) {
			t.Errorf("%s on Windows: %v, and want an error that names it and wraps ErrUnsupported", tc.name, err)
		}
		free(t, tc.name+"'s refusal", name)
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
	l, err := Listen(soon(t), local(t))
	if err != nil {
		t.Fatalf("no option on Windows: %v, and want it to listen", err)
	}
	if err := l.Close(); err != nil {
		t.Error(err)
	}
}

// free fails t unless name is a pipe's name nothing holds: Listen takes it.
func free(t *testing.T, what, name string) {
	t.Helper()
	l, err := Listen(soon(t), name)
	if err != nil {
		t.Errorf("after %s, %s is not free: %v", what, name, err)
		return
	}
	if err := l.Close(); err != nil {
		t.Error(err)
	}
}

// On Windows Listen takes a pipe's name alone, \\.\pipe\ and a name with no
// separator that does not end in a dot or a space: a path, relative or
// absolute, another machine's pipe, a device path and a name Win32 would
// change are each refused with ErrPipeName, naming it, and nothing is made.
// The prefix is matched as Windows matches it, whatever its case.
//
// What it costs when this fails: a program configured for Unix makes a socket
// in a folder on Windows that no access list here guards, or a name ending in
// a dot listens at another name than the one it was given, or ..\ climbs out
// of the pipes' namespace.
func TestOnWindowsAPathOrAnotherNameIsRefusedWithErrPipeName(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cases := []string{
		`.\app.sock`,
		`..\app.sock`,
		`./app.sock`,
		`/tmp/app.sock`,
		filepath.Join(dir, "app.sock"),
		`\app.sock`,
		`\\.\pipe\`,
		`\\.\pipe\a\b`,
		`\\.\pipe\..\..\c$\app`,
		`\\.\pipe\a/b`,
		`\\.\pipe\app.`,
		`\\.\pipe\app `,
		`\\.\pipe\..`,
		`\\server\pipe\app`,
		`\\?\pipe\app`,
		`\\.\pip\app`,
	}
	ran := 0
	for _, addr := range cases {
		ran++
		l, err := Listen(soon(t), addr)
		if err == nil {
			_ = l.Close()
		}
		if !errors.Is(err, ErrPipeName) || !strings.Contains(fmt.Sprint(err), strconv.Quote(addr)) {
			t.Errorf("Listen(%q) on Windows: %v, and want ErrPipeName, naming it", addr, err)
		}
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
	if es, err := os.ReadDir(dir); err != nil || len(es) != 0 {
		t.Errorf("a refused name made %d files in the folder (%v)", len(es), err)
	}
	name := local(t)
	upper := `\\.\PIPE\` + strings.TrimPrefix(name, pipePrefix)
	l, err := Listen(soon(t), upper)
	if err != nil {
		t.Fatalf("%s: %v, and want it to listen", upper, err)
	}
	defer l.Close()
	if _, err := Listen(soon(t), name); !errors.Is(err, ErrHeld) {
		t.Errorf("%s while %s listens: %v, and want ErrHeld, the one pipe", name, upper, err)
	}
}

// A pipe already at the name is refused with ErrHeld and never joined, by
// whoever made it: a pipe this process made with Listen, one made with no
// access list of listen's, and one another process holds, which still answers
// after. Once Listen's own pipe closes, with no connection ever made to it,
// its name is free at once: nothing of it is left.
//
// What it costs when this fails: another account makes the pipe first, with
// an access list of its own, and the program serves as its second instance:
// the squatter takes the program's callers, or reads what they send.
func TestAPipeAlreadyAtTheNameIsRefusedWithErrHeld(t *testing.T) {
	name := local(t)
	first, err := Listen(soon(t), name)
	if err != nil {
		t.Fatal(err)
	}
	if l, err := Listen(soon(t), name); !errors.Is(err, ErrHeld) {
		if err == nil {
			_ = l.Close()
		}
		t.Errorf("a second Listen at %s: %v, and want ErrHeld", name, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	free(t, "Close", name)

	squat := local(t)
	other, err := winio.ListenPipe(squat, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l, err := Listen(soon(t), squat); !errors.Is(err, ErrHeld) {
		if err == nil {
			_ = l.Close()
		}
		t.Errorf("Listen at %s, a pipe made with another access list: %v, and want ErrHeld", squat, err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}

	// serial from here: it starts a child process, in a folder whose name is
	// its own, as its pipe's is.
	dir, err := os.MkdirTemp(t.TempDir(), "child")
	if err != nil {
		t.Fatal(err)
	}
	c := startChild(t, dir)
	if said := c.first(t); said != "listening" {
		t.Fatalf("the child said %q", said)
	}
	if l, err := Listen(soon(t), childAt(dir)); !errors.Is(err, ErrHeld) {
		if err == nil {
			_ = l.Close()
		}
		t.Errorf("Listen at %s, which another process holds: %v, and want ErrHeld", childAt(dir), err)
	}
	conn, err := dialLocal(soon(t), childAt(dir))
	if err != nil {
		t.Errorf("the child's pipe after the refusal: %v, and want it to answer", err)
	} else {
		_ = conn.Close()
	}
	if code := c.stop(t); code != 0 {
		t.Errorf("the child exited %d", code)
	}
}

// A pipe whose holder bounded it to one instance, as .NET's
// NamedPipeServerStream does by default, is refused with ErrHeld and never
// joined: Windows answers the first server end's FILE_CREATE there with
// ERROR_PIPE_BUSY, its one instance being made, and not ERROR_ACCESS_DENIED.
// The squatter is another process, Windows PowerShell, and the name is free
// once it exits.
//
// What it costs when this fails: a program finds its name held by a squatter
// and is told something other than ErrHeld, so a caller that waits out or
// reports a held name does neither, though the name is held all the same.
func TestAOneInstancePipeAtTheNameIsRefusedWithErrHeld(t *testing.T) {
	t.Parallel()
	name := local(t)
	short := strings.TrimPrefix(name, pipePrefix)
	const script = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$pipe = New-Object System.IO.Pipes.NamedPipeServerStream($env:GO_SOCKET_PIPE, [System.IO.Pipes.PipeDirection]::InOut, 1)
try {
  [Console]::Out.WriteLine('made')
  [Console]::Out.Flush()
  [void][Console]::In.ReadLine()
} finally {
  $pipe.Dispose()
}`
	cmd := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "GO_SOCKET_PIPE="+short)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	said, _ := bufio.NewReader(stdout).ReadString('\n')
	if strings.TrimSpace(said) != "made" {
		// stderr is read once PowerShell is gone, so exec's copier is done
		// writing it.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("PowerShell said %q, and want made: %s", said, stderr.String())
	}
	l, err := Listen(soon(t), name)
	if err == nil {
		_ = l.Close()
	}
	if !errors.Is(err, ErrHeld) {
		t.Errorf("Listen at %s, a pipe of one instance another process made: %v, and want ErrHeld", name, err)
	}
	// ERROR_PIPE_BUSY itself, so the row is red with 231 taken out of the
	// mapping, and red too if Windows answers otherwise, which the doc would
	// then have wrong.
	if !errors.Is(err, errorPipeBusy) {
		t.Errorf("Listen at %s, a pipe of one instance another process made: %v, and want ERROR_PIPE_BUSY (231) under ErrHeld", name, err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("PowerShell: %v: %s", err, stderr.String())
	}
	free(t, "the one-instance pipe's holder exited", name)
}

// A client that keeps its end of a pipe held nothing once the pipe's server
// ends were closed, the listener and the connection it accepted, as after
// the program exited, on Windows 11 (build 26200), where the tests met it:
// the name went with the last server end. A new Listen there takes the name,
// as a restarted program's would, and the client's old end reaches nothing:
// a write on it fails, and the new pipe's first Accept is the client that
// dialed it. A Windows that kept the name would fail this row at that
// Listen with ErrHeld, a refusal: the docs say not to count on either.
//
// What it costs when this fails: a client of the run before reaches the
// restarted program's pipe without dialing it, past the new pipe's access
// list and peer's look at who connected; or, on this build, a restarted
// program cannot take its name while a client of the run before keeps its
// end open.
func TestAClientThatKeepsItsEndHoldsNothingOnceTheServerEndsClose(t *testing.T) {
	t.Parallel()
	name := local(t)
	l, err := Listen(soon(t), name)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			err = c.Close()
		}
		accepted <- err
	}()
	stale, err := dialLocal(soon(t), name)
	if err != nil {
		_ = l.Close()
		t.Fatal(err)
	}
	if err := <-accepted; err != nil {
		_ = stale.Close()
		_ = l.Close()
		t.Fatalf("the accepted connection: %v", err)
	}
	if err := l.Close(); err != nil {
		_ = stale.Close()
		t.Fatal(err)
	}
	again, err := Listen(soon(t), name)
	if err != nil {
		_ = stale.Close()
		t.Fatalf("Listen at %s once its server ends closed, while a client keeps its end: %v, and want the name", name, err)
	}
	if _, err := stale.Write([]byte("s")); err == nil {
		t.Errorf("a client of the closed pipe wrote once a new Listen took %s, and want an error", name)
	} else {
		// The OS's refusal, ERROR_NO_DATA or ERROR_PIPE_NOT_CONNECTED, and
		// not os.ErrClosed, which would mean the handle was closed here.
		t.Logf("the client's old end, written once a new Listen took %s: %v", name, err)
	}
	ctx := soon(t)
	first := make(chan string, 1)
	go func() { first <- firstByte(ctx, again) }()
	fresh, err := dialLocal(ctx, name)
	if err != nil {
		_ = again.Close()
		_ = stale.Close()
		t.Fatal(err)
	}
	if _, err := fresh.Write([]byte("f")); err != nil {
		t.Errorf("the new pipe's own client wrote: %v", err)
	}
	if got := <-first; got != "f" {
		t.Errorf("the new pipe's first Accept at %s read %q, and want the byte its own client wrote, \"f\"", name, got)
	}
	for _, c := range []interface{ Close() error }{fresh, again, stale} {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}
	free(t, "every end's close", name)
}

// firstByte is the byte the first connection l accepts reads first, or why
// none was read by ctx's deadline.
func firstByte(ctx context.Context, l net.Listener) string {
	c, err := l.Accept()
	if err != nil {
		return "nothing, Accept: " + err.Error()
	}
	defer c.Close()
	if d, ok := ctx.Deadline(); ok {
		if err := c.SetReadDeadline(d); err != nil {
			return "nothing, the deadline: " + err.Error()
		}
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(c, b); err != nil {
		return "nothing, the read: " + err.Error()
	}
	return string(b)
}

// A connection Accept gave holds the pipe's name past its listener's Close,
// as a server end of the program's own: a new Listen there is ErrHeld and
// never joined, and the name is free once that connection closes, while its
// client still keeps its end. That last is Windows 11 build 26200's answer,
// where the name was seen free once the last server end closed: on a build
// that keeps the name while a client holds its end, this row goes red at
// free while Listen still fails closed there, with ErrHeld.
//
// What it costs when this fails: a Listen joins the pipe of a program that
// still serves a client on it, as that pipe's next instance under the other
// program's access list and not its own; or the name is not free once the
// program's own ends are closed.
func TestAConnectionAcceptGaveHoldsThePipesNamePastItsListener(t *testing.T) {
	t.Parallel()
	name := local(t)
	l, err := Listen(soon(t), name)
	if err != nil {
		t.Fatal(err)
	}
	conns := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			t.Errorf("Accept: %v", err)
		}
		conns <- c
	}()
	client, err := dialLocal(soon(t), name)
	if err != nil {
		_ = l.Close()
		t.Fatal(err)
	}
	conn := <-conns
	if err := l.Close(); err != nil {
		t.Error(err)
	}
	if conn == nil {
		_ = client.Close()
		t.FailNow()
	}
	if again, err := Listen(soon(t), name); !errors.Is(err, ErrHeld) {
		if err == nil {
			_ = again.Close()
		}
		t.Errorf("Listen at %s while a connection Accept gave is open, its listener closed: %v, and want ErrHeld", name, err)
	}
	if err := conn.Close(); err != nil {
		t.Error(err)
	}
	free(t, "the accepted connection's close, its client's end still open", name)
	if err := client.Close(); err != nil {
		t.Error(err)
	}
}

// noneAt is noPipeAt(name), failing t on its error.
func noneAt(t *testing.T, name string) bool {
	t.Helper()
	none, err := noPipeAt(name)
	if err != nil {
		t.Errorf("noPipeAt(%s): %v", name, err)
	}
	return none
}

// Windows answers that no pipe is at a name nothing holds, and never at a
// name a listener holds, whatever the prefix's case, nor at a name holding a
// character FindFirstFile takes as a wildcard: noPipeAt is true for a free
// name alone, and again once the listener closes.
//
// What it costs when this fails: a Listen refused at a name another
// program's pipe holds says the token may not make a pipe, and the program's
// operator looks for a missing right, not for the program that holds the
// name; or a free name's refusal is never told from a held one's.
func TestNoPipeAtIsTrueForAFreeNameAlone(t *testing.T) {
	t.Parallel()
	name := local(t)
	if !noneAt(t, name) {
		t.Errorf("noPipeAt(%s) with no pipe there: false, and want true", name)
	}
	l, err := Listen(soon(t), name)
	if err != nil {
		t.Fatal(err)
	}
	if noneAt(t, name) {
		t.Errorf("noPipeAt(%s) while a listener holds it: true, and want false", name)
	}
	if upper := `\\.\PIPE\` + name[len(pipePrefix):]; noneAt(t, upper) {
		t.Errorf("noPipeAt(%s) while a listener holds it: true, and want false", upper)
	}
	if err := l.Close(); err != nil {
		t.Error(err)
	}
	if !noneAt(t, name) {
		t.Errorf("noPipeAt(%s) once its listener closed: false, and want true", name)
	}
	for _, c := range []string{`*`, `?`, `<`, `>`, `"`} {
		if n := local(t) + "a" + c + "b"; noneAt(t, n) {
			t.Errorf("noPipeAt(%s), a name holding %s: true, and want false", n, c)
		}
	}
}

// An access denied at a name no pipe is at, as a token that may make no pipe
// meets it (Windows 11 build 26200), wraps fs.ErrPermission
// and not ErrHeld; at a name a pipe holds, and at a name holding a wildcard,
// it is ErrHeld, as ERROR_PIPE_BUSY is anywhere; any other error is
// returned as it is.
//
// What it costs when this fails: a program refused a pipe for a right it
// lacks is told another program holds the name; or a held name's refusal
// comes without ErrHeld, and a caller that tests ErrHeld first, as the doc
// asks, takes another program's pipe for a right it lacks.
func TestAnAccessDeniedWithNoPipeAtTheNameIsErrPermission(t *testing.T) {
	t.Parallel()
	denied := func(name string) error {
		return &os.PathError{Op: "open", Path: name, Err: syscall.ERROR_ACCESS_DENIED}
	}
	none := local(t)
	err := refusal(none, denied(none))
	if !errors.Is(err, fs.ErrPermission) || errors.Is(err, ErrHeld) || !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Errorf("access denied at %s, no pipe there: %v, and want fs.ErrPermission wrapping ERROR_ACCESS_DENIED, never ErrHeld", none, err)
	}
	if err := refusal(none, &os.PathError{Op: "open", Path: none, Err: errorPipeBusy}); !errors.Is(err, ErrHeld) {
		t.Errorf("ERROR_PIPE_BUSY at %s, no pipe there: %v, and want ErrHeld", none, err)
	}
	wild := local(t) + "a*b"
	if err := refusal(wild, denied(wild)); !errors.Is(err, ErrHeld) {
		t.Errorf("access denied at %s, a name holding a wildcard: %v, and want ErrHeld", wild, err)
	}
	other := errors.New("something else")
	if err := refusal(none, other); err != other { //nolint:errorlint // the very error is returned, not one that wraps it
		t.Errorf("another error at %s: %v, and want it returned as it is", none, err)
	}
	held := local(t)
	l, err := Listen(soon(t), held)
	if err != nil {
		t.Fatal(err)
	}
	if err := refusal(held, denied(held)); !errors.Is(err, ErrHeld) {
		t.Errorf("access denied at %s while a listener holds it: %v, and want ErrHeld", held, err)
	}
	if err := l.Close(); err != nil {
		t.Error(err)
	}
}

// localFolder is a folder for a test's Local, removed when the test ends.
func localFolder(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// localWant is the address Local gives for name in folder, an absolute folder, on Windows, as
// Local's doc gives it: the pipe's name, a dash and the first 16 hex digits of the SHA-256 of
// the folder.
func localWant(_ *testing.T, folder, name string) string {
	sum := sha256.Sum256([]byte(folder))
	return pipePrefix + name + "-" + hex.EncodeToString(sum[:])[:16]
}
