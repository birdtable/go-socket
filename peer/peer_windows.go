package peer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"unsafe"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// impersonateNamedPipeClient is the one call peer makes that x/sys does not
// wrap, loaded from System32 alone, once, as x/sys loads its own.
var impersonateNamedPipeClient = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient") //nolint:gochecknoglobals // arch.ports.12: a proc is loaded once, as x/sys keeps its own

// pipePrefix is where every name ListenPipe and DialPipe take starts: a pipe
// on this machine.
const pipePrefix = `\\.\pipe\`

// The rights of a pipe's access list. On a pipe the bit of FILE_APPEND_DATA,
// 0x4, is FILE_CREATE_PIPE_INSTANCE: whoever holds it may make a server end of
// the pipe and take its clients.
const (
	// clientAccess is what each listed SID may do, and all DialPipe asks for:
	// read and write the pipe, and read its owner. It holds no 0x4.
	clientAccess = windows.FILE_GENERIC_READ | windows.FILE_WRITE_DATA
	// listenerAccess is the pipe's owner's: go-winio opens the
	// pipe again, for reading and writing, to make the next server end at each
	// Accept, which needs the generic rights and 0x4 with them.
	listenerAccess = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE
)

var (
	// errPipeName is a name that is not a pipe on this machine: another
	// machine's pipe, which DialPipe would hand this account's identity to,
	// a path that leaves \\.\pipe\ for a file, or a name Win32 would open
	// as another, since it drops a name's trailing dots and spaces.
	errPipeName = errors.New(`a pipe's name is \\.\pipe\ and then a name with no \ or / in it that does not end in a dot or a space`)
	// errNoSID is ListenPipe given no SID: a pipe only SYSTEM may open.
	errNoSID = errors.New("a pipe is opened by the SIDs it is given, and none was given: name at least one")
	// errSIDForm is a SID not written as Windows writes it back: an SDDL
	// alias such as WD, a part with a sign or a leading zero, or anything
	// after it, an access entry of its own among them.
	errSIDForm = errors.New("not a SID as Windows writes it, S-1- and then decimal parts with no sign or leading zero")
	// errSIDNobody is a SID that names every caller or none, so a pipe
	// listing it would be opened by anybody. The seven are the common
	// mistakes, not every wide group: This Organization (S-1-5-15), Local
	// account (S-1-5-113), Console Logon (S-1-2-1), Remote Interactive Logon
	// (S-1-5-14), Batch (S-1-5-3), Service (S-1-5-6), NTLM Authentication
	// (S-1-5-64-10) and a domain's Domain Users are taken, and how wide a
	// group is stays the caller's to judge: Listener with Principals is the
	// check that names a caller.
	errSIDNobody = errors.New(`the null SID, Everyone, ANONYMOUS LOGON, Authenticated Users, BUILTIN\Users, INTERACTIVE and LOCAL name every caller or none, and no pipe lists them`)
	// errNotPipe is PeerOf on a connection that is not one of go-winio's
	// named pipes, which Windows keeps no account for.
	errNotPipe = fmt.Errorf("the connection is not a named pipe's, so Windows keeps no account for it: %w", ErrCannotSay)
	// errAnonymous is a pipe's client that dialled at the anonymous level, as
	// go-winio's own dials do: Windows names nobody for it.
	errAnonymous = fmt.Errorf("the pipe's client dialled anonymously, as go-winio's own dials do, so Windows names nobody for it; dial with peer.DialPipe: %w", ErrCannotSay)
	// errDenyOnly is a pipe's client whose token holds its user for deny
	// alone: the token may not act as that account, so it is not named as it.
	errDenyOnly = fmt.Errorf("the pipe's client holds its user for deny alone, so it is not that account: %w", ErrCannotSay)
)

// ListenPipe listens on the named pipe name, \\.\pipe\ and then a name that
// does not end in a dot or a space, which Win32 drops, that
// SYSTEM, the pipe's owner and the accounts sids name alone may open, each
// SID written as Windows writes one ("S-1-5-21-...-1001"). The pipe's access
// list is set as the pipe is made, so there is no moment it is open to anyone
// else: SYSTEM may do anything; the pipe's owner, the default owner of the
// process's token (the listener's own account, or BUILTIN\Administrators for
// an elevated administrator), may read, write and make the pipe's next server
// end, as go-winio does at each Accept; each SID may read and write
// and make no server end; and the kernel refuses everyone else at the dial.
// So no listed account can serve the name and take a client: on the dialling
// side PeerOf names the pipe's owner, and only the pipe's owner may make a
// server end. A pipe of that name already there, made by anybody, is
// an error, never joined. go-winio refuses clients on other machines. PeerOf
// says which account each connection is; Listener with Principals keeps the
// ones a program trusts. No SID, a SID written another way, and a SID that
// names every caller or none (the null SID, Everyone, ANONYMOUS LOGON,
// Authenticated Users, BUILTIN\Users, INTERACTIVE, LOCAL) are refused. On
// other systems ListenPipe is ErrCannotSay.
func ListenPipe(name string, sids ...string) (net.Listener, error) {
	if err := checkPipeName(name); err != nil {
		return nil, err
	}
	owner, err := processOwner()
	if err != nil {
		return nil, err
	}
	sddl, err := pipeSDDL(owner, sids)
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: sddl})
}

// DialPipe dials the named pipe name, \\.\pipe\ and then a name that does not
// end in a dot or a space, on this
// machine, asking for a listed account's rights and no more: to read and
// write the pipe and read its owner. It dials at the identification level,
// so the listener can read who this account is and cannot act as it.
// It dials with static context tracking, so the listener reads the account
// that dialled for the life of the connection.
// go-winio's own dials are anonymous, and PeerOf on the listener's side
// refuses them. PeerOf on the connection DialPipe returns is the account that
// owns the pipe. On other systems DialPipe is ErrCannotSay.
func DialPipe(ctx context.Context, name string) (net.Conn, error) {
	if err := checkPipeName(name); err != nil {
		return nil, err
	}
	return winio.DialPipeAccessImpLevel(ctx, name, clientAccess, winio.PipeImpLevelIdentification)
}

// tokenOwner is TOKEN_OWNER, which x/sys does not declare.
type tokenOwner struct {
	Owner *windows.SID
}

// processOwner is the default owner of the process's token: the SID Windows
// makes the owner of each pipe ListenPipe makes, since its access list names
// no owner, and so the account PeerOf names on the dialling side. It is the
// account itself, or BUILTIN\Administrators for an elevated administrator,
// whose unelevated processes hold that group for deny alone.
func processOwner() (string, error) {
	t := windows.GetCurrentProcessToken()
	for n := uint32(64); ; {
		// The buffer is words, not bytes, so TOKEN_OWNER's pointer sits where
		// a pointer may by its type, not by where the allocator puts a []byte.
		words := (n + 7) / 8
		b := make([]uint64, words)
		had := words * 8
		err := windows.GetTokenInformation(t, windows.TokenOwner, (*byte)(unsafe.Pointer(&b[0])), had, &n) //nolint:gosec // G103: the word buffer is passed as the bytes GetTokenInformation fills
		if errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) && n > had {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("the owner the listener's token gives what it makes could not be read: %w", err)
		}
		s := (*tokenOwner)(unsafe.Pointer(&b[0])).Owner.String() //nolint:gosec // G103: TOKEN_OWNER is a pointer into the buffer GetTokenInformation filled, as x/sys reads TOKEN_USER
		runtime.KeepAlive(b)
		if s == "" {
			return "", errors.New("the owner the listener's token gives what it makes could not be written as a SID")
		}
		return s, nil
	}
}

// checkPipeName refuses a name that is not \\.\pipe\ and then a name with no
// separator in it, and a name that ends in a dot or a space: Win32 drops
// those from the last part of a path, so \\.\pipe\svc. would open
// \\.\pipe\svc, and . or .. alone would resolve out of \\.\pipe\.
func checkPipeName(name string) error {
	if len(name) <= len(pipePrefix) || !strings.EqualFold(name[:len(pipePrefix)], pipePrefix) {
		return fmt.Errorf("%q: %w", name, errPipeName)
	}
	rest := name[len(pipePrefix):]
	if strings.ContainsAny(rest, `\/`) || strings.HasSuffix(rest, ".") || strings.HasSuffix(rest, " ") {
		return fmt.Errorf("%q: %w", name, errPipeName)
	}
	return nil
}

// pipeSDDL is the access list of a pipe that SYSTEM, the pipe's owner owner
// and sids alone may open, protected so nothing is inherited into it:
// SYSTEM's entry, owner's with listenerAccess, then one entry per SID with
// clientAccess, in order. A SID that is owner as well keeps both entries, and
// owner's rights with them. Each SID is screened by sidEntries.
func pipeSDDL(owner string, sids []string) (string, error) {
	if len(sids) == 0 {
		return "", errNoSID
	}
	var b strings.Builder
	b.WriteString("D:P(A;;GA;;;SY)")
	fmt.Fprintf(&b, "(A;;%#x;;;%s)", listenerAccess, owner)
	if err := sidEntries(&b, sids, fmt.Sprintf("%#x", clientAccess)); err != nil {
		return "", err
	}
	return b.String(), nil
}

// sidEntries writes one access entry to b per SID of sids, each allowing
// rights and no other, in order, and marked for no inheritance. Each SID is
// parsed, and kept only if Windows writes the parsed SID back as it was
// given, so no SDDL alias and nothing after the SID reaches the list; a SID
// that names every caller or none is refused by SID (errSIDNobody). ListenPipe
// screens its SIDs here alone.
func sidEntries(b *strings.Builder, sids []string, rights string) error {
	// nobody is the seven SIDs that name every caller or none, the common
	// mistakes, not every wide group (errSIDNobody).
	nobody := [...]windows.WELL_KNOWN_SID_TYPE{
		windows.WinNullSid,
		windows.WinWorldSid,
		windows.WinAnonymousSid,
		windows.WinAuthenticatedUserSid,
		windows.WinBuiltinUsersSid,
		windows.WinInteractiveSid,
		windows.WinLocalSid,
	}
	for _, s := range sids {
		sid, err := windows.StringToSid(s)
		if err != nil || sid.String() != s {
			return fmt.Errorf("%q: %w", s, errSIDForm)
		}
		if slices.ContainsFunc(nobody[:], sid.IsWellKnown) {
			return fmt.Errorf("%q: %w", s, errSIDNobody)
		}
		fmt.Fprintf(b, "(A;;%s;;;%s)", rights, s)
	}
	return nil
}

// peerOf is the account at the other end of c, one of go-winio's named pipes.
// On the listener's side it is the client's, read by clientSID; on the
// dialling side it is the account that owns the pipe. Windows gives no
// process this package trusts here, so PIDKnown is false.
func peerOf(c net.Conn) (Peer, error) {
	if _, ok := c.(winio.PipeConn); !ok {
		return Peer{}, errNotPipe
	}
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return Peer{}, errNotPipe
	}
	h := windows.Handle(f.Fd())
	var flags uint32
	if err := windows.GetNamedPipeInfo(h, &flags, nil, nil, nil); err != nil {
		return Peer{}, fmt.Errorf("%w: %w", errNotPipe, err)
	}
	if flags&windows.PIPE_SERVER_END == 0 {
		return pipeOwner(h)
	}
	type answer struct {
		sid string
		err error
	}
	got := make(chan answer, 1)
	go func() {
		sid, err := clientSID(h)
		got <- answer{sid, err}
	}()
	a := <-got
	if a.err != nil {
		return Peer{}, a.err
	}
	return Peer{User: a.sid}, nil
}

// pipeOwner is the account that owns the pipe h is the client end of: the
// account whose listener made it.
func pipeOwner(h windows.Handle) (Peer, error) {
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return Peer{}, fmt.Errorf("the pipe's owner could not be read: %w: %w", ErrCannotSay, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return Peer{}, fmt.Errorf("the pipe's owner could not be read: %w: %w", ErrCannotSay, err)
	}
	s := owner.String()
	if s == "" {
		return Peer{}, fmt.Errorf("the pipe's owner could not be written as a SID: %w", ErrCannotSay)
	}
	return Peer{User: s}, nil
}

// clientSID is the SID of the account at the client end of the pipe h, read
// from the client's token on a thread of its own: it impersonates the client,
// reads the user of the thread's token, and reverts. It runs on the goroutine
// peerOf starts for it, locked to its thread, which it unlocks only once the
// thread is the process's own again; if it cannot say so, it panics on that
// goroutine, which no caller can recover, so a thread that stays someone else
// never runs another line.
func clientSID(h windows.Handle) (string, error) {
	runtime.LockOSThread()
	sid, err := impersonatedUser(h)
	if rerr := windows.RevertToSelf(); rerr != nil {
		panic(fmt.Sprintf("peer: RevertToSelf failed, and this thread may still be the pipe's client: %v", rerr)) //nolint:forbidigo // go.errors.3: a thread that stays someone else must not run on
	}
	if hook := reverted.Load(); hook != nil {
		(*hook)(h)
	}
	var t windows.Token
	if terr := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &t); !errors.Is(terr, windows.ERROR_NO_TOKEN) {
		if terr == nil {
			terr = errors.Join(errors.New("it has one"), t.Close())
		}
		panic(fmt.Sprintf("peer: after RevertToSelf this thread still has a token, or cannot say: %v", terr)) //nolint:forbidigo // go.errors.3: a thread that stays someone else must not run on
	}
	runtime.UnlockOSThread()
	return sid, err
}

// impersonatedUser impersonates the client of the pipe h on this thread and
// reads the user of the thread's token. The caller reverts, whether it
// succeeded or not.
func impersonatedUser(h windows.Handle) (string, error) {
	// BOOL is 32 bits: the lower half of r alone is the answer.
	if r, _, err := impersonateNamedPipeClient.Call(uintptr(h)); r&0xffffffff == 0 {
		return "", fmt.Errorf("the pipe's client could not be impersonated: %w: %w", ErrCannotSay, err)
	}
	var t windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &t); err != nil {
		if errors.Is(err, windows.ERROR_CANT_OPEN_ANONYMOUS) {
			return "", fmt.Errorf("%w: %w", errAnonymous, err)
		}
		return "", fmt.Errorf("the pipe's client's token could not be opened: %w: %w", ErrCannotSay, err)
	}
	sid, err := tokenUser(t)
	if cerr := t.Close(); cerr != nil {
		return "", errors.Join(err, fmt.Errorf("the pipe's client's token could not be closed: %w: %w", ErrCannotSay, cerr))
	}
	return sid, err
}

// tokenUser is the SID of the user of the token t, unless t holds its user
// for deny alone (SE_GROUP_USE_FOR_DENY_ONLY), as a restricted token may:
// such a token cannot act as the user, so it is refused, never named.
func tokenUser(t windows.Token) (string, error) {
	u, err := t.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("the pipe's client's user could not be read: %w: %w", ErrCannotSay, err)
	}
	if u.User.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY != 0 {
		return "", errDenyOnly
	}
	s := u.User.Sid.String()
	if s == "" {
		return "", fmt.Errorf("the pipe's client's user could not be written as a SID: %w", ErrCannotSay)
	}
	return s, nil
}

// reverted is set by peer's own tests alone, in peer_windows_test.go: no
// line of the package outside a test writes it, and it is unexported, so no
// program can. clientSID runs it on the impersonating thread, once
// RevertToSelf has returned and before the thread is unlocked, with the
// pipe's handle. It is not gated by testing.Testing(), so a program that links
// peer does not link package testing.
var reverted atomic.Pointer[func(windows.Handle)] //nolint:gochecknoglobals // arch.ports.12: a test's seam, unexported and written only by peer's tests; PeerOf takes no options to carry it
