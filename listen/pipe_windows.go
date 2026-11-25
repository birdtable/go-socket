package listen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"

	winio "github.com/Microsoft/go-winio"
)

// pipePrefix is where every pipe name Listen takes starts: a pipe on this
// machine.
const pipePrefix = `\\.\pipe\`

// listen is Listen's options as given. A pipe takes none of them: each is
// kept so that unixAlone refuses it by name, and the pipe is made in one
// call, with nothing between for a test seam to look at.
type listen struct {
	mode os.FileMode
	// modeSet says SocketMode was given, whatever the mode: the default
	// alone is silent.
	modeSet    bool
	group      string
	evenIfBusy bool
}

// listenLocal is a named pipe of the program's own at the name addr, by
// Listen's rules, or why not. ctx bounds nothing here: a pipe is made at
// once, with no connection asked of a run before's.
func listenLocal(_ context.Context, addr string, o listen) (net.Listener, error) {
	if err := pipeName(addr); err != nil {
		return nil, err
	}
	if err := unixAlone(o); err != nil {
		return nil, err
	}
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("the account the pipe is given to: %w", err)
	}
	sddl, err := pipeSDDL(u.Uid)
	if err != nil {
		return nil, err
	}
	l, err := winio.ListenPipe(addr, &winio.PipeConfig{SecurityDescriptor: sddl})
	if err != nil {
		return nil, refusal(addr, err)
	}
	return l, nil
}

// refusal is Listen's error for err, ListenPipe's at addr. go-winio makes the
// first server end with FILE_CREATE, and Windows answers it at a name that is
// already a pipe, any process's, with ERROR_ACCESS_DENIED (0x5), never
// ERROR_ALREADY_EXISTS, as a Windows run of the tests showed; or, where the
// pipe's holder gave it a bounded count of instances and every one is made,
// with ERROR_PIPE_BUSY (231), which on a FILE_CREATE can mean only that a pipe
// is there. Either is ErrHeld. A token that may make no pipe meets
// ERROR_ACCESS_DENIED at a free name too (seen on Windows 11 build 26200
// under a restricted token): that one wraps fs.ErrPermission, and only once
// Windows answers that no pipe is at the name (noPipeAt). Every other
// ERROR_ACCESS_DENIED stays ErrHeld, and any other error is returned as it is.
func refusal(addr string, err error) error {
	denied := errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	none, lookErr := false, error(nil)
	if denied {
		none, lookErr = noPipeAt(addr)
	}
	if errors.Is(err, errorPipeBusy) || denied && !none {
		return errors.Join(fmt.Errorf("%s is a pipe already: %w (%w)", addr, ErrHeld, err), lookErr)
	}
	if denied {
		return fmt.Errorf("no pipe is at %s, and this token may not make one: %w (%w)", addr, fs.ErrPermission, err)
	}
	return err
}

// noPipeAt says Windows answered that no pipe is at name: FindFirstFile on
// the pipe's full name, which lists it from the pipe file system's folder,
// connects to nothing and takes no instance, failed with ERROR_NO_MORE_FILES
// (18), as it does for a free name on Windows 11 build 26200, where a held
// name is found. Every other answer
// is false, so a name a pipe may hold is never reported as free: a pipe
// found; ERROR_FILE_NOT_FOUND, which no Windows here was seen to answer; a
// folder this process may not list; and a name holding a character
// FindFirstFile takes as a wildcard (* ? < > "), which may match another
// pipe's name and not its own. It asks on a goroutine of its own, whose
// thread impersonates nothing, so the process's own token asks, as a caller's
// thread may impersonate one that may not list. A pipe made or closed between
// the refusal and this ask is read as it is then: Listen has refused either
// way.
//
// The error is what kept it from asking, or the found handle's close: either
// comes with false, and is the caller's to report beside its refusal.
func noPipeAt(name string) (bool, error) {
	if strings.ContainsAny(name[len(pipePrefix):], `*?<>"`) {
		return false, nil
	}
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return false, fmt.Errorf("whether a pipe is at %q: %w", name, err)
	}
	type answer struct {
		none bool
		err  error
	}
	asked := make(chan answer, 1)
	go func() {
		var d syscall.Win32finddata
		h, err := syscall.FindFirstFile(p, &d)
		if err != nil {
			asked <- answer{none: errors.Is(err, syscall.ERROR_NO_MORE_FILES)}
			return
		}
		if err := syscall.FindClose(h); err != nil {
			asked <- answer{err: fmt.Errorf("whether a pipe is at %s, the search's close: %w", name, err)}
			return
		}
		asked <- answer{}
	}()
	a := <-asked
	return a.none, a.err
}

// errorPipeBusy is ERROR_PIPE_BUSY, which the standard syscall package does
// not name.
const errorPipeBusy syscall.Errno = 231

// pipeName refuses a name that is not \\.\pipe\ and then a name with no
// separator in it, and a name that ends in a dot or a space: Win32 drops
// those from the last part of a path, so \\.\pipe\svc. would open
// \\.\pipe\svc, and . or .. alone would resolve out of \\.\pipe\. A path is
// refused the same way, since a Unix socket is not what Listen makes here.
func pipeName(name string) error {
	if len(name) <= len(pipePrefix) || !strings.EqualFold(name[:len(pipePrefix)], pipePrefix) {
		return fmt.Errorf(`%q: on Windows Listen takes a pipe's name, \\.\pipe\ and then a name, and no path: %w`, name, ErrPipeName)
	}
	rest := name[len(pipePrefix):]
	if strings.ContainsAny(rest, `\/`) || strings.HasSuffix(rest, ".") || strings.HasSuffix(rest, " ") {
		return fmt.Errorf("%q: %w", name, ErrPipeName)
	}
	return nil
}

// localAt is Local's address on Windows: the pipe name, a dash and the first
// 16 hex digits of the SHA-256 of folder made absolute. name is refused as
// Listen would refuse it in a pipe's name, so one ending in a dot or a space
// is refused though the digits follow it.
func localAt(folder, name string) (string, error) {
	if err := pipeName(pipePrefix + name); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(folder)
	if err != nil {
		return "", fmt.Errorf("%s made absolute: %w", folder, err)
	}
	sum := sha256.Sum256([]byte(abs))
	return pipePrefix + name + "-" + hex.EncodeToString(sum[:8]), nil
}

// unixAlone refuses each option that means something for a Unix socket
// alone: a pipe has an access list, not a mode or a group, and leaves no file
// behind to replace.
func unixAlone(o listen) error {
	var errs []error
	if o.group != "" {
		errs = append(errs, fmt.Errorf("SocketGroup(%q): a pipe has no group, only its access list: %w", o.group, errors.ErrUnsupported))
	}
	if o.modeSet {
		errs = append(errs, fmt.Errorf("SocketMode(%#o): a pipe has no mode, only its access list: %w", uint32(o.mode), errors.ErrUnsupported))
	}
	if o.evenIfBusy {
		errs = append(errs, fmt.Errorf("ReplaceEvenIfBusy: a pipe leaves no file to replace: %w", errors.ErrUnsupported))
	}
	return errors.Join(errs...)
}

// pipeSDDL is the pipe's owner and access list: the account sid owns it, so
// an elevated administrator's pipe is the account's and not
// BUILTIN\Administrators', which the token would make its owner otherwise;
// and the list is protected so nothing is inherited into it: SYSTEM may do
// anything, and the account may read and write the pipe (FILE_GENERIC_READ
// and FILE_GENERIC_WRITE, whose FILE_APPEND_DATA bit is
// FILE_CREATE_PIPE_INSTANCE on a pipe, so go-winio can make the next server
// end at each Accept). sid is parsed and kept only if Windows writes the
// parsed SID back as it was given, so no SDDL alias and nothing after the SID
// reaches the descriptor.
func pipeSDDL(sid string) (string, error) {
	parsed, err := syscall.StringToSid(sid)
	if err != nil {
		return "", fmt.Errorf("the account %q the pipe is given to is not a SID: %w", sid, err)
	}
	back, err := parsed.String()
	if err != nil {
		return "", fmt.Errorf("the account %q the pipe is given to, written back: %w", sid, err)
	}
	if back != sid {
		return "", fmt.Errorf("the account %q the pipe is given to is not a SID as Windows writes it, %q", sid, back)
	}
	return "O:" + sid + "D:P(A;;GA;;;SY)(A;;FRFW;;;" + sid + ")", nil
}
