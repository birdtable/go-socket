package listen

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Local's answer is what Listen takes, and a client given the same string reaches the listener:
// a socket's path on Unix, and on Windows a pipe's name, dialled through go-winio.
//
// What it costs when this fails: a program serves at an address Local gave and its client, told
// that address as SSH_AUTH_SOCK, reaches nothing, or Listen refuses what Local answered.
func TestLocalsAddressIsListenedOnAndReachedByAClientGivenIt(t *testing.T) {
	t.Parallel()
	addr, err := Local(localFolder(t), "agent")
	if err != nil {
		t.Fatal(err)
	}
	l, err := Listen(soon(t), addr)
	if err != nil {
		t.Fatalf("Listen(%q), as Local answered: %v", addr, err)
	}
	defer func() { _ = l.Close() }()
	if !l.Local() || l.Name() != addr {
		t.Errorf("the listener at Local's %q is local %v, named %q", addr, l.Local(), l.Name())
	}
	got := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			got <- err.Error()
			return
		}
		defer func() { _ = c.Close() }()
		b, _ := io.ReadAll(io.LimitReader(c, 5))
		got <- string(b)
	}()
	c, err := dialLocal(soon(t), addr)
	if err != nil {
		t.Fatalf("a client given %q: %v", addr, err)
	}
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if s := <-got; s != "hello" {
		t.Errorf("the listener at %q read %q from the client, and want %q", addr, s, "hello")
	}
}

// One folder and name give one address, every time; two folders with one name give two, on
// Windows as on Unix, where pipe names are the machine's.
//
// What it costs when this fails: two programs, or two tests, in folders of their own take one
// pipe on Windows, and the second is refused with ErrHeld, or its client reaches the first; or
// a client told the address again reaches nothing.
func TestTwoFoldersWithOneNameGiveTwoAddresses(t *testing.T) {
	t.Parallel()
	one, two := localFolder(t), localFolder(t)
	a, err := Local(one, "agent")
	if err != nil {
		t.Fatal(err)
	}
	again, err := Local(one, "agent")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Local(two, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if a != again {
		t.Errorf("Local(%q, agent) twice: %q and %q, and want one address", one, a, again)
	}
	if a == b {
		t.Errorf("Local(%q, agent) and Local(%q, agent) are both %q, and want two addresses", one, two, a)
	}
	if want := localWant(t, one, "agent"); a != want {
		t.Errorf("Local(%q, agent) is %q, and want %q", one, a, want)
	}
}

// A relative folder is made absolute against the folder the program is in, so its address is
// the absolute folder's.
//
// What it costs when this fails: a program that moves to another folder after Local is told an
// address its client, started elsewhere, cannot reach, or two programs in two folders that both
// pass "." share a pipe.
func TestARelativeFolderGivesTheAbsoluteFoldersAddress(t *testing.T) {
	dir := localFolder(t)
	t.Chdir(dir)
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := Local(".", "agent")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := Local(here, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if rel != abs {
		t.Errorf("Local(., agent) in %s is %q, and Local(%q, agent) is %q: want one address", here, rel, here, abs)
	}
}

// A name that is not one plain name is refused, quoting it, and nothing is made: empty with
// ErrNoAddress, . and .., a separator of either system and a NUL on every system, and on Windows
// one ending in a dot or a space, which Listen refuses in a pipe's name, with ErrPipeName.
//
// What it costs when this fails: a name with a separator puts the socket outside the caller's
// folder, or on Windows into another pipe's name; . or .. names the folder or the one above;
// a NUL cuts the name short at the system; and on Windows "agent." listens where Win32 opens
// "agent".
func TestEachNameThatIsNotOnePlainNameIsRefused(t *testing.T) {
	t.Parallel()
	windows := runtime.GOOS == "windows"
	cases := []struct {
		name    string
		refused bool
		is      error
	}{
		{"", true, ErrNoAddress},
		{".", true, nil},
		{"..", true, nil},
		{"a/b", true, nil},
		{`a\b`, true, nil},
		{"/agent", true, nil},
		{`\agent`, true, nil},
		{"agent\x00", true, nil},
		{"agent.", windows, errIf(windows, ErrPipeName)},
		{"agent ", windows, errIf(windows, ErrPipeName)},
		{"agent.sock", false, nil},
		{".agent", false, nil},
	}
	dir := localFolder(t)
	ran := 0
	for _, tc := range cases {
		ran++
		addr, err := Local(dir, tc.name)
		switch {
		case !tc.refused && err != nil:
			t.Errorf("Local(%q, %q): %v, and want an address", dir, tc.name, err)
		case tc.refused && err == nil:
			t.Errorf("Local(%q, %q) is %q, and want it refused", dir, tc.name, addr)
		case tc.refused && !containsQuoted(err, tc.name):
			t.Errorf("Local(%q, %q): %v, and want the name quoted", dir, tc.name, err)
		case tc.is != nil && !errors.Is(err, tc.is):
			t.Errorf("Local(%q, %q): %v, and want %v", dir, tc.name, err, tc.is)
		}
	}
	if ran != len(cases) {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
	if es, err := os.ReadDir(dir); err != nil || len(es) != 0 {
		t.Errorf("after Local's refusals the folder holds %d names (%v), and want none", len(es), err)
	}
}

// errIf is err where on, and nil elsewhere.
func errIf(on bool, err error) error {
	if on {
		return err
	}
	return nil
}

// containsQuoted says whether err's text quotes name as Go quotes a string.
func containsQuoted(err error, name string) bool {
	return err != nil && strings.Contains(fmt.Sprint(err), strconv.Quote(name))
}
