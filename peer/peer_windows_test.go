package peer

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unsafe"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// pipeName is a pipe name no other test, and no other run, uses: 26
// random letters and digits after the process's number.
func pipeName() string {
	return fmt.Sprintf(`\\.\pipe\peer-test-%d-%s`, os.Getpid(), rand.Text())
}

// self is the SID of the account the test runs as.
func self(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

// stranger is a SID no account of this machine has: a domain of its own that
// no machine was given, so a listing of it lets nobody the test runs as in.
const stranger = "S-1-5-21-1-2-3-4242"

// accepted is one Accept's answer.
type accepted struct {
	c   net.Conn
	err error
}

// acceptOne runs l's Accept once on a goroutine of its own and hands its
// answer to the channel it returns.
func acceptOne(l net.Listener) <-chan accepted {
	got := make(chan accepted, 1)
	go func() {
		c, err := l.Accept()
		got <- accepted{c, err}
	}()
	return got
}

// listen is ListenPipe on a fresh name, closed when the test ends.
func listen(t *testing.T, sids ...string) (net.Listener, string) {
	t.Helper()
	name := pipeName()
	l, err := ListenPipe(name, sids...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, name
}

// pair is a server and a client end of one pipe on l, the client dialled by
// dial, both closed when the test ends.
func pair(t *testing.T, l net.Listener, name string, dial func(string) (net.Conn, error)) (server, client net.Conn) {
	t.Helper()
	got := acceptOne(l)
	client, err := dial(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	a := <-got
	if a.err != nil {
		t.Fatal(a.err)
	}
	t.Cleanup(func() { _ = a.c.Close() })
	return a.c, client
}

func dialNamed(t *testing.T) func(string) (net.Conn, error) {
	t.Helper()
	return func(name string) (net.Conn, error) { return DialPipe(t.Context(), name) }
}

func dialAnonymous(t *testing.T) func(string) (net.Conn, error) {
	t.Helper()
	return func(name string) (net.Conn, error) { return winio.DialPipeContext(t.Context(), name) }
}

// The listener reads its client's SID from the client's token, as the client
// connected, before a byte of the connection is read: the client here writes
// none. PIDKnown is false: Windows' answer is not one this package trusts.
//
// What it costs when this fails: a Windows daemon cannot name the account at
// the other end of its pipe, or names it only after it has read a request.
func TestPeerOfAPipeIsItsClientsSIDBeforeAByteIsRead(t *testing.T) {
	t.Parallel()
	me := self(t)
	l, name := listen(t, me)
	server, _ := pair(t, l, name, dialNamed(t))
	p, err := PeerOf(server)
	if err != nil || p != (Peer{User: me}) {
		t.Errorf("PeerOf = %+v, %v, want the user %s and no process", p, err, me)
	}
}

// go-winio's own dial is anonymous: Windows names nobody for it, so PeerOf
// refuses it by name, and Listener closes it and tells refused that no
// principal was read.
//
// What it costs when this fails: a client that dialled with go-winio's
// default is taken as somebody, or refused with an error that does not say
// how to dial.
func TestAnAnonymousDialIsRefusedByName(t *testing.T) {
	t.Parallel()
	me := self(t)
	l, name := listen(t, me)
	server, _ := pair(t, l, name, dialAnonymous(t))
	p, err := PeerOf(server)
	if !errors.Is(err, errAnonymous) || !errors.Is(err, ErrCannotSay) || p != (Peer{}) {
		t.Errorf("PeerOf = %+v, %v, want no peer and the error that names the anonymous dial", p, err)
	}

	gl, gname := listen(t, me)
	refused := make(chan refusal, 1)
	g := Listener(gl, Principals(me), func(principal string, known bool) { refused <- refusal{principal, known} })
	got := acceptOne(g)
	client, err := winio.DialPipeContext(t.Context(), gname)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if r := <-refused; r != (refusal{"", false}) {
		t.Errorf("refused was told %+v, want no principal and known false", r)
	}
	if n, err := client.Read(make([]byte, 1)); err == nil {
		t.Errorf("the refused client read %d bytes and no error: its connection is still open", n)
	}
	_ = gl.Close()
	if a := <-got; a.c != nil || !errors.Is(a.err, net.ErrClosed) {
		t.Errorf("Accept = %v, %v, want no connection and the closed listener's error", a.c, a.err)
	}
}

// refusal is one call of a Listener's refused.
type refusal struct {
	principal string
	known     bool
}

// After PeerOf has impersonated a client, named or anonymous, the thread it
// did so on has no token: OpenThreadToken on it is ERROR_NO_TOKEN.
//
// What it costs when this fails: a thread of the program runs on as the
// pipe's client, and whatever it does next is done as that account.
func TestTheImpersonatingThreadIsItselfAgain(t *testing.T) {
	t.Parallel()
	var (
		mu   sync.Mutex
		seen = map[windows.Handle]error{}
	)
	hook := func(h windows.Handle) {
		var tok windows.Token
		err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &tok)
		if err == nil {
			_ = tok.Close()
		}
		mu.Lock()
		seen[h] = err
		mu.Unlock()
	}
	reverted.Store(&hook)
	t.Cleanup(func() { reverted.Store(nil) })
	me := self(t)
	for _, d := range []struct {
		name string
		dial func(string) (net.Conn, error)
	}{{"named", dialNamed(t)}, {"anonymous", dialAnonymous(t)}} {
		l, name := listen(t, me)
		server, _ := pair(t, l, name, d.dial)
		_, _ = PeerOf(server)
		h := windows.Handle(server.(interface{ Fd() uintptr }).Fd())
		mu.Lock()
		err, ok := seen[h]
		mu.Unlock()
		if !ok {
			t.Errorf("%s dial: PeerOf never reverted on the thread it impersonated on", d.name)
		} else if !errors.Is(err, windows.ERROR_NO_TOKEN) {
			t.Errorf("%s dial: OpenThreadToken after PeerOf = %v, want ERROR_NO_TOKEN", d.name, err)
		}
	}
}

// On the dialling side PeerOf is the account that owns the pipe: the one
// whose listener made it, here the test's own.
//
// What it costs when this fails: a client cannot tell that the pipe it
// dialled was made by the daemon's account and not by another that took the
// name first.
func TestPeerOfTheDiallingSideIsThePipesOwner(t *testing.T) {
	t.Parallel()
	me := self(t)
	l, name := listen(t, me)
	_, client := pair(t, l, name, dialNamed(t))
	p, err := PeerOf(client)
	if err != nil || p != (Peer{User: me}) {
		t.Errorf("PeerOf on the dialling side = %+v, %v, want the owner %s", p, err, me)
	}
}

// A thousand times over one name: listen, Accept on a goroutine, dial once,
// and close the client, the server end and the listener. Every close returns
// and the name is free again for the next round. The loop is bound by its
// count, not by a clock: a close that hangs holds the test until go test's
// own timeout, and a close that leaves the pipe makes the next ListenPipe
// fail; a time limit would add only a machine's speed.
//
// What it costs when this fails: a daemon that restarts its listener hangs
// in Close, or cannot listen again on its own name.
func TestAThousandListensEachCloseReturns(t *testing.T) {
	t.Parallel()
	me := self(t)
	name := pipeName()
	const rounds = 1000
	closed := 0
	for i := range rounds {
		l, err := ListenPipe(name, me)
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		got := acceptOne(l)
		client, err := DialPipe(t.Context(), name)
		if err != nil {
			_ = l.Close()
			t.Fatalf("round %d: %v", i, err)
		}
		a := <-got
		if a.err != nil {
			t.Fatalf("round %d: %v", i, a.err)
		}
		for _, c := range []struct {
			what string
			c    interface{ Close() error }
		}{{"client", client}, {"server end", a.c}, {"listener", l}} {
			if err := c.c.Close(); err != nil {
				t.Fatalf("round %d: closing the %s: %v", i, c.what, err)
			}
			closed++
		}
	}
	if closed != 3*rounds {
		t.Fatalf("%d closes returned, want %d", closed, 3*rounds)
	}
}

// ListenPipe refuses a pipe no SID may open, a SID not written as Windows
// writes it, an SDDL entry of its own after a SID among them, and a SID that
// names every caller or none; ListenPipe and DialPipe both refuse a name that
// is not a pipe on this machine. Nothing is made at a refused name.
//
// What it costs when this fails: a pipe is opened by everybody, an access
// entry rides in on a SID, or this account's identity goes to another
// machine's pipe.
func TestListenPipeAndDialPipeRefuse(t *testing.T) {
	t.Parallel()
	me := self(t)
	for _, c := range []struct {
		sids []string
		want error
	}{
		{nil, errNoSID},
		{[]string{"SY"}, errSIDForm},
		{[]string{"WD"}, errSIDForm},
		{[]string{"s-1-5-18"}, errSIDForm},
		{[]string{"S-1-5-018"}, errSIDForm},
		{[]string{" " + me}, errSIDForm},
		{[]string{me + ")(A;;GA;;;WD)"}, errSIDForm},
		{[]string{me, ""}, errSIDForm},
		{[]string{"S-1-0-0"}, errSIDNobody},
		{[]string{"S-1-1-0"}, errSIDNobody},
		{[]string{"S-1-5-7"}, errSIDNobody},
		{[]string{me, "S-1-5-11"}, errSIDNobody},
		{[]string{"S-1-5-32-545"}, errSIDNobody},
		{[]string{"S-1-5-4"}, errSIDNobody},
		{[]string{me, "S-1-2-0"}, errSIDNobody},
	} {
		name := pipeName()
		if l, err := ListenPipe(name, c.sids...); !errors.Is(err, c.want) {
			if l != nil {
				_ = l.Close()
			}
			t.Errorf("ListenPipe(%q) = %v, want %v", c.sids, err, c.want)
		}
		if _, err := winio.DialPipeContext(t.Context(), name); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("ListenPipe(%q) refused, yet a dial of its name is %v, want no such pipe", c.sids, err)
		}
	}
	for _, name := range []string{
		`\\server\pipe\peer-test`,
		`\\?\pipe\peer-test`,
		`\\.\pipe\`,
		`\\.\pipe\.`,
		`\\.\pipe\..`,
		`\\.\pipe\..\..\C:\Windows\win.ini`,
		`\\.\pipe\a\b`,
		`\\.\pipe\a/b`,
		`\\.\pipe\...`,
		`\\.\pipe\ `,
		`\\.\pipe\svc.`,
		`//./pipe/peer-test`,
		`C:\Windows\win.ini`,
		`peer-test`,
	} {
		if l, err := ListenPipe(name, me); !errors.Is(err, errPipeName) {
			if l != nil {
				_ = l.Close()
			}
			t.Errorf("ListenPipe(%q) = %v, want %v", name, err, errPipeName)
		}
		if c, err := DialPipe(t.Context(), name); !errors.Is(err, errPipeName) {
			if c != nil {
				_ = c.Close()
			}
			t.Errorf("DialPipe(%q) = %v, want %v", name, err, errPipeName)
		}
	}
	if l, err := ListenPipe(`\\.\PIPE\`+pipeName()[len(pipePrefix):], me); err != nil {
		t.Errorf("ListenPipe of a name with PIPE in capitals = %v, want it taken as Windows takes it", err)
	} else {
		_ = l.Close()
	}
}

// createRestrictedToken is advapi32's CreateRestrictedToken, which x/sys
// does not wrap, loaded from System32 alone as peer loads its own proc.
var createRestrictedToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken") //nolint:gochecknoglobals // arch.ports.12: a proc is loaded once, as x/sys keeps its own

// other is a SID no account has and no test's pipe lists.
const other = "S-1-5-21-1-2-3-4243"

// restrictedTo is the test's own token restricted to the SID sid, as a token
// a thread may impersonate. An access check on a restricted token passes only
// for what both its account and sid are granted, so on a pipe the account's
// own entry, the listener's, gives it nothing that sid's entry does not: the
// token holds sid's entry alone. No second account is needed.
func restrictedTo(t *testing.T, sid string) windows.Token {
	t.Helper()
	return restricted(t, nil, sid)
}

// sids is each SID of ss, parsed, as CreateRestrictedToken takes a list of
// them: nil and 0 for none.
func sids(t *testing.T, ss []string) ([]windows.SIDAndAttributes, uintptr) {
	t.Helper()
	if len(ss) == 0 {
		return nil, 0
	}
	l := make([]windows.SIDAndAttributes, 0, len(ss))
	for _, s := range ss {
		sid, err := windows.StringToSid(s)
		if err != nil {
			t.Fatal(err)
		}
		l = append(l, windows.SIDAndAttributes{Sid: sid})
	}
	return l, uintptr(unsafe.Pointer(&l[0])) //nolint:gosec // G103: a proc x/sys does not wrap takes its pointers as uintptr, as peer's own does
}

// restricted is the test's own token with the SIDs of disable held for deny
// alone and restricted to the SIDs of to, as a token a thread may
// impersonate, whose default access list a test may set.
func restricted(t *testing.T, disable []string, to ...string) windows.Token {
	t.Helper()
	var p windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &p); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	off, offp := sids(t, disable)
	only, onlyp := sids(t, to)
	var r windows.Token
	// BOOL is 32 bits: the lower half of ok alone is the answer. The lists
	// are kept alive past the call below.
	if ok, _, err := createRestrictedToken.Call(uintptr(p), 0, uintptr(len(off)), offp, 0, 0, uintptr(len(only)), onlyp, uintptr(unsafe.Pointer(&r))); ok&0xffffffff == 0 { //nolint:gosec // G103: a proc x/sys does not wrap takes its pointers as uintptr, as peer's own does
		t.Fatalf("CreateRestrictedToken: %v", err)
	}
	runtime.KeepAlive(off)
	runtime.KeepAlive(only)
	defer func() { _ = r.Close() }()
	var imp windows.Token
	if err := windows.DuplicateTokenEx(r, windows.TOKEN_IMPERSONATE|windows.TOKEN_QUERY|windows.TOKEN_ADJUST_DEFAULT, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &imp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = imp.Close() })
	return imp
}

// as runs do on a thread of its own that impersonates tok, once the thread's
// own token is seen to be a restricted one, with a restricting list or its
// user held for deny alone, so a refusal do meets is the
// token's and not a thread's that never became it. A thread Windows let
// identify the token alone would meet ERROR_BAD_IMPERSONATION_LEVEL, not a
// refusal of access, and a dial as it would fail.
// do must not call t.Fatal: the thread reverts after it.
func as(t *testing.T, tok windows.Token, do func()) {
	t.Helper()
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if err := windows.SetThreadToken(nil, tok); err != nil {
			runtime.UnlockOSThread()
			errc <- fmt.Errorf("SetThreadToken: %w", err)
			return
		}
		err := impersonating()
		if err == nil {
			do()
		}
		if rerr := windows.RevertToSelf(); rerr != nil {
			// The thread stays locked, and ends with this goroutine.
			errc <- errors.Join(err, fmt.Errorf("RevertToSelf: %w", rerr))
			return
		}
		runtime.UnlockOSThread()
		errc <- err
	}()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

// impersonating is nil when this thread's own token is a restricted one, with
// a restricting list or its user held for deny alone, and says what it is
// otherwise.
func impersonating() error {
	var th windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &th); err != nil {
		return fmt.Errorf("the thread has no token of its own: %w", err)
	}
	defer func() { _ = th.Close() }()
	r, err := th.IsRestricted()
	if err != nil {
		return fmt.Errorf("whether the thread's token is restricted could not be read: %w", err)
	}
	if r {
		return nil
	}
	u, err := th.GetTokenUser()
	if err != nil {
		return fmt.Errorf("the thread's token's user could not be read: %w", err)
	}
	if u.User.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 {
		return errors.New("the thread's token is not restricted and holds its user enabled")
	}
	return nil
}

// serverEnd makes a server end of the pipe name, as an account the pipe lists
// would ask for one: NtCreateNamedPipeFile opening the pipe that is there,
// shared for reading and writing as go-winio's are, asking for a listed
// entry's rights alone, FILE_READ_DATA | FILE_WRITE_DATA | SYNCHRONIZE. It
// closes the end it makes.
func serverEnd(name string) error {
	nt, err := windows.NewNTUnicodeString(`\??\pipe\` + name[len(pipePrefix):])
	if err != nil {
		return err
	}
	oa := windows.OBJECT_ATTRIBUTES{ObjectName: nt}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var (
		h    windows.Handle
		iosb windows.IO_STATUS_BLOCK
	)
	timeout := int64(-50 * 10000) // 50ms, go-winio's
	if err := windows.NtCreateNamedPipeFile(&h, windows.FILE_READ_DATA|windows.FILE_WRITE_DATA|windows.SYNCHRONIZE, &oa, &iosb,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN, 0, windows.FILE_PIPE_REJECT_REMOTE_CLIENTS, 0, 0,
		0xffffffff, 0, 0, &timeout); err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

// A token that holds a listed SID's entry alone, the test's own restricted to
// stranger, asking for no more than that entry gives, cannot make a server end
// of the pipe ListenPipe made: its entry, 0x12008b, is refused. On a pipe
// made as ListenPipe makes one but with 0x4 added to that entry, 0x12008f,
// the same call by the same token makes one. So 0x4, FILE_CREATE_PIPE_INSTANCE,
// is what refuses it, and not a right the call asks for that the entry lacks.
//
// What it costs when this fails: an account the pipe lists can serve its
// name and take the next client, and the client's PeerOf names the pipe's
// owner for it.
func TestAListedAccountCannotMakeAServerEnd(t *testing.T) {
	t.Parallel()
	owner, err := processOwner()
	if err != nil {
		t.Fatal(err)
	}
	sddl, err := pipeSDDL(owner, []string{stranger})
	if err != nil {
		t.Fatal(err)
	}
	listed := fmt.Sprintf("(A;;%#x;;;%s)", clientAccess, stranger)
	withCreate := strings.Replace(sddl, listed, fmt.Sprintf("(A;;%#x;;;%s)", clientAccess|0x4, stranger), 1)
	if !strings.Contains(sddl, "(A;;0x12008b;;;"+stranger+")") || !strings.Contains(withCreate, "(A;;0x12008f;;;"+stranger+")") {
		// Not fatal: with 0x4 put back in clientAccess, the refusal below
		// must go red too: putting 0x4 back is this guard's break.
		t.Errorf("the listed entry is not 0x12008b in %s, or 0x12008f in %s", sddl, withCreate)
	}
	_, name := listen(t, stranger)
	control := pipeName()
	l, err := winio.ListenPipe(control, &winio.PipeConfig{SecurityDescriptor: withCreate})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	tok := restrictedTo(t, stranger)
	var refused, allowed error
	as(t, tok, func() {
		refused = serverEnd(name)
		allowed = serverEnd(control)
	})
	if !errors.Is(refused, windows.STATUS_ACCESS_DENIED) {
		t.Errorf("a server end of ListenPipe's pipe, holding a listed SID's entry (0x12008b) alone = %v, want access denied", refused)
	}
	if allowed != nil {
		t.Errorf("a server end of a pipe whose listed entry holds 0x4 (0x12008f) = %v, want one made: the refusal above is not 0x4's", allowed)
	}
}

// heldGroup is a group the test's own token holds enabled that a pipe may
// list: not held for deny alone, not an integrity level, and not one that
// names every caller or none. The logon session's SID is one most tokens hold.
func heldGroup(t *testing.T) string {
	t.Helper()
	g, err := windows.GetCurrentProcessToken().GetTokenGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range g.AllGroups() {
		if a.Attributes&windows.SE_GROUP_ENABLED == 0 || a.Attributes&(windows.SE_GROUP_USE_FOR_DENY_ONLY|windows.SE_GROUP_INTEGRITY) != 0 {
			continue
		}
		s := a.Sid.String()
		if _, err := pipeSDDL(s, []string{s}); err == nil {
			return s
		}
	}
	t.Fatal("the test's token holds no enabled group a pipe may list")
	return ""
}

// A client whose token holds its user for deny alone, the test's own with its
// user among the SIDs to disable, dialling through a group the pipe lists and
// the token still holds, is refused on the listener's side: PeerOf is
// ErrCannotSay and never names the user, so a Listener refuses it.
//
// What it costs when this fails: a token that may not act as an account is
// named as it, and a Listener trusting that account lets it in.
func TestADenyOnlyUserIsRefusedNeverNamed(t *testing.T) {
	t.Parallel()
	me := self(t)
	group := heldGroup(t)
	l, name := listen(t, group)
	got := acceptOne(l)
	var (
		client net.Conn
		err    error
	)
	as(t, restricted(t, []string{me}, group), func() { client, err = DialPipe(t.Context(), name) })
	if err != nil {
		t.Fatalf("DialPipe holding the user for deny alone, through the listed group %s = %v, want a connection", group, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	a := <-got
	if a.err != nil {
		t.Fatal(a.err)
	}
	t.Cleanup(func() { _ = a.c.Close() })
	if p, err := PeerOf(a.c); !errors.Is(err, errDenyOnly) || !errors.Is(err, ErrCannotSay) || p != (Peer{}) {
		t.Errorf("PeerOf of a client holding its user %s for deny alone = %+v, %v, want %v", me, p, err, errDenyOnly)
	}
}

// tokenDefaultDACL is TOKEN_DEFAULT_DACL, which x/sys does not declare.
type tokenDefaultDACL struct {
	DefaultDacl *windows.ACL
}

// systemAlone sets the default access list of tok to one entry, SYSTEM's, and
// reads it back: what the token makes from then on, a copy of itself among
// them, only SYSTEM may open.
func systemAlone(t *testing.T, tok windows.Token) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString("D:(A;;GA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	set := tokenDefaultDACL{dacl}
	if err := windows.SetTokenInformation(tok, uint32(windows.TokenDefaultDacl), (*byte)(unsafe.Pointer(&set)), uint32(unsafe.Sizeof(set))); err != nil { //nolint:gosec // G103: TOKEN_DEFAULT_DACL is passed as the bytes it is, as x/sys passes its own
		t.Fatalf("SetTokenInformation(TokenDefaultDacl): %v", err)
	}
	runtime.KeepAlive(sd)
	var n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenDefaultDacl, nil, 0, &n); !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		t.Fatalf("GetTokenInformation(TokenDefaultDacl) for its size: %v, want %v", err, windows.ERROR_INSUFFICIENT_BUFFER)
	}
	// The buffer is words, not bytes, so TOKEN_DEFAULT_DACL's pointer sits
	// where a pointer may: a []byte has only a byte's alignment, and checkptr
	// aborts the conversion below under -race when it lands off a word.
	b := make([]uint64, (uintptr(n)+7)/8)
	size := uint32(uintptr(len(b)) * unsafe.Sizeof(b[0]))
	if err := windows.GetTokenInformation(tok, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&b[0])), size, &n); err != nil { //nolint:gosec // G103: the word buffer is passed as the bytes GetTokenInformation fills
		t.Fatalf("GetTokenInformation(TokenDefaultDacl): %v", err)
	}
	got := (*tokenDefaultDACL)(unsafe.Pointer(&b[0])).DefaultDacl //nolint:gosec // G103: TOKEN_DEFAULT_DACL points into the buffer GetTokenInformation filled
	if got == nil || got.AceCount != 1 {
		t.Fatalf("the token's default access list, read back, is not SYSTEM's one entry: %+v", got)
	}
	runtime.KeepAlive(b)
}

// A client whose token's default access list names SYSTEM alone is still
// named on the listener's side. PeerOf opens the impersonated thread's token
// as the listener's own process, and the token the listener holds for its
// client is a copy Windows made of the client's: this shows the copy's access
// list does not keep the listener out.
//
// What it costs when this fails: every client of another account that sets
// its own default access list, as a service may, is ErrCannotSay, and a
// Listener refuses it.
func TestAClientWhoseTokenLetsSYSTEMAloneOpenWhatItMakesIsNamed(t *testing.T) {
	t.Parallel()
	me := self(t)
	l, name := listen(t, stranger)
	tok := restricted(t, nil, stranger)
	systemAlone(t, tok)
	got := acceptOne(l)
	var (
		client net.Conn
		err    error
	)
	as(t, tok, func() { client, err = DialPipe(t.Context(), name) })
	if err != nil {
		t.Fatalf("DialPipe with a default access list of SYSTEM alone = %v, want a connection", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	a := <-got
	if a.err != nil {
		t.Fatal(a.err)
	}
	t.Cleanup(func() { _ = a.c.Close() })
	if p, err := PeerOf(a.c); err != nil || p != (Peer{User: me}) {
		t.Errorf("PeerOf of a client whose token's default access list is SYSTEM's alone = %+v, %v, want the user %s", p, err, me)
	}
}

// A pipe made by a thread whose token holds its user for deny alone, with an
// access list that names no owner, is made by Windows and owned by the user:
// the dialling side's PeerOf names the user for it, as the README says, and
// the deny-only refusal is the listener's side's alone. The token has no
// restricting list: on Windows 11 a token restricted to a group it holds is
// refused every pipe, so the deny-only user alone is what
// differs from the process.
//
// What it costs when this fails: the README promises the dialling side names
// the account whatever token of it made the pipe, and that is no longer so.
func TestAPipeMadeByADenyOnlyUserIsNamedAsItsAccount(t *testing.T) {
	t.Parallel()
	me := self(t)
	group := heldGroup(t)
	name := pipeName()
	// The group may do anything, so go-winio's later instances, made on its
	// own goroutine as the process, are made; the user may dial.
	sddl := fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;%s)(A;;%#x;;;%s)", group, clientAccess, me)
	var (
		l   net.Listener
		err error
	)
	as(t, restricted(t, []string{me}), func() {
		l, err = winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: sddl})
	})
	if err != nil {
		t.Fatalf("ListenPipe under a token holding its user for deny alone = %v, want the pipe made", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	got := acceptOne(l)
	client, err := DialPipe(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if a := <-got; a.err != nil {
		t.Fatal(a.err)
	} else {
		t.Cleanup(func() { _ = a.c.Close() })
	}
	p, err := PeerOf(client)
	if err != nil || p.User != me {
		t.Errorf("PeerOf on the dialling side of a pipe made by a token holding its user %s for deny alone = %+v, %v; want {User:%s}", me, p, err, me)
	}
}

// A token that holds a listed SID's entry alone dials with DialPipe, writes
// and reads; the listener names its account, and the dialling side the
// pipe's owner. DialPipe asks for no right the entry does not give.
//
// What it costs when this fails: DialPipe asks for more than a listed
// account holds, and every client but the listener's own account is refused.
func TestAListedAccountDialsWithAClientsRightsAlone(t *testing.T) {
	t.Parallel()
	me := self(t)
	l, name := listen(t, stranger)
	got := acceptOne(l)
	var (
		client net.Conn
		err    error
	)
	as(t, restrictedTo(t, stranger), func() { client, err = DialPipe(t.Context(), name) })
	if err != nil {
		t.Fatalf("DialPipe holding a listed SID's entry alone = %v, want a connection", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	a := <-got
	if a.err != nil {
		t.Fatal(a.err)
	}
	t.Cleanup(func() { _ = a.c.Close() })
	if p, err := PeerOf(a.c); err != nil || p != (Peer{User: me}) {
		t.Errorf("PeerOf on the listener's side = %+v, %v, want the user %s", p, err, me)
	}
	if p, err := PeerOf(client); err != nil || p != (Peer{User: me}) {
		t.Errorf("PeerOf on the dialling side = %+v, %v, want the owner %s", p, err, me)
	}
	for _, d := range []struct {
		what     string
		from, to net.Conn
	}{{"client to listener", client, a.c}, {"listener to client", a.c, client}} {
		go func() { _, _ = d.from.Write([]byte(d.what)) }()
		b := make([]byte, len(d.what))
		if _, err := io.ReadFull(d.to, b); err != nil || string(b) != d.what {
			t.Errorf("%s: read %q, %v, want %q", d.what, b, err, d.what)
		}
	}
}

// The kernel refuses, at the dial, an account the pipe does not list, and the
// listener accepts nothing. The account is the test's own, restricted to a
// SID the pipe does not list, which leaves it no use of the listener's own
// entry.
//
// What it costs when this fails: a pipe listing one account is opened by
// another, and only a Listener in front of it stands between that account
// and the program.
func TestAnAccountThePipeDoesNotListIsRefusedAtTheDial(t *testing.T) {
	t.Parallel()
	l, name := listen(t, stranger)
	got := acceptOne(l)
	var (
		c   net.Conn
		err error
	)
	as(t, restrictedTo(t, other), func() { c, err = DialPipe(t.Context(), name) })
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		if c != nil {
			_ = c.Close()
		}
		t.Errorf("DialPipe as an account the pipe does not list = %v, want access denied", err)
	}
	_ = l.Close()
	if a := <-got; a.c != nil || !errors.Is(a.err, net.ErrClosed) {
		t.Errorf("Accept = %v, %v, want nothing accepted", a.c, a.err)
	}
}

// The access list read back from the pipe with GetSecurityInfo is the SDDL
// built from the SIDs, as the kernel keeps it: protected, so nothing is
// inherited into it; SYSTEM with every right (GA, which a file object keeps
// as FA); the pipe's owner, as GetSecurityInfo reads it, with read, write and
// the right to make the next server end (0x12019f), so the one account that
// may serve the name is the one PeerOf names on the dialling side; then one entry for each SID ListenPipe was
// given, in order, with read and write alone (0x12008b); and nothing else: no
// Everyone, no SID it was not given. No listed SID's entry holds 0x4, which
// on a pipe is FILE_CREATE_PIPE_INSTANCE.
//
// What it costs when this fails: a pipe is made with an entry nobody asked
// for, an account no program listed opens it, or a listed one may serve it.
func TestThePipesAccessListIsSYSTEMTheListenerAndEachSIDAlone(t *testing.T) {
	t.Parallel()
	me := self(t)
	sids := []string{me, stranger}
	l, name := listen(t, sids...)
	_, client := pair(t, l, name, dialNamed(t))
	h := windows.Handle(client.(interface{ Fd() uintptr }).Fd())
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("the access list is not protected (control %#x): an entry may be inherited into it", control)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("the pipe's access list = %v, %v, want one", dacl, err)
	}
	if n := int(dacl.AceCount); n != 2+len(sids) {
		t.Errorf("the access list holds %d entries, want SYSTEM's, the listener's and %d more", n, len(sids))
	}
	for i := uint32(2); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Mask&windows.FILE_APPEND_DATA != 0 {
			t.Errorf("entry %d, a listed SID's, holds %#x, with 0x4, FILE_CREATE_PIPE_INSTANCE: it may make a server end", i, ace.Mask)
		}
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		t.Fatalf("the pipe's owner = %v, %v, want one", owner, err)
	}
	built := "O:" + owner.String() + "D:P(A;;FA;;;SY)(A;;0x12019f;;;" + owner.String() + ")"
	for _, s := range sids {
		built += "(A;;0x12008b;;;" + s + ")"
	}
	// Windows writes a well-known SID back by its alias, BA for
	// BUILTIN\Administrators, the owner of an elevated listener's pipe.
	wantSD, err := windows.SecurityDescriptorFromString(built)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sd.String(), wantSD.String(); got != want {
		t.Errorf("the pipe's access list is\n%s\nwant\n%s", got, want)
	}
}

// Principals takes a SID as it takes a Unix principal, and the gate works
// over a pipe listener unchanged: the account it lists is handed on, and an
// account it does not list, though the pipe lets it in, is closed and told
// to refused by its SID.
//
// What it costs when this fails: a Windows daemon cannot keep its gate to
// the accounts it trusts, or a refusal does not say who was refused.
func TestTheGateWorksOverAPipeListener(t *testing.T) {
	t.Parallel()
	me := self(t)

	l, name := listen(t, me)
	g := Listener(l, Principals(me), func(principal string, known bool) {
		t.Errorf("refused %q, known %v, want the listed account handed on", principal, known)
	})
	server, _ := pair(t, g, name, dialNamed(t))
	if p, err := PeerOf(server); err != nil || p.User != me {
		t.Errorf("PeerOf of the accepted connection = %+v, %v, want %s", p, err, me)
	}

	ol, oname := listen(t, me)
	refused := make(chan refusal, 1)
	og := Listener(ol, Principals(stranger), func(principal string, known bool) { refused <- refusal{principal, known} })
	got := acceptOne(og)
	client, err := DialPipe(t.Context(), oname)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if r := <-refused; r != (refusal{me, true}) {
		t.Errorf("refused was told %+v, want %s, known", r, me)
	}
	if n, err := client.Read(make([]byte, 1)); err == nil {
		t.Errorf("the refused client read %d bytes and no error: its connection is still open", n)
	}
	_ = ol.Close()
	if a := <-got; a.c != nil || !errors.Is(a.err, net.ErrClosed) {
		t.Errorf("Accept = %v, %v, want no connection and the closed listener's error", a.c, a.err)
	}
}

// PeerOf on a connection that is not a named pipe's is ErrCannotSay.
//
// What it costs when this fails: a TCP connection on loopback, or a test's
// in-memory pipe, is taken for a Windows account.
func TestPeerOfAConnectionThatIsNotAPipeCannotSay(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	if p, err := PeerOf(server); !errors.Is(err, errNotPipe) || !errors.Is(err, ErrCannotSay) || p != (Peer{}) {
		t.Errorf("PeerOf = %+v, %v, want no peer and ErrCannotSay", p, err)
	}
}
