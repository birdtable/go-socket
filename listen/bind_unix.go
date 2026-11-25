//go:build unix

package listen

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/docker/go-connections/sockets"
)

// backlog is the queue Listen asks of the kernel, which holds it to its own
// somaxconn (Linux's net.core.somaxconn, kern.ipc.somaxconn on macOS and the
// BSDs), and so takes the most the system allows.
const backlog = 1<<16 - 1

// nonceLen is the bytes of the nonce own's dial writes.
const nonceLen = 32

// bindAt binds a socket in a private folder beside path, gives it no access
// and then what given says, listens on it, and only then puts it at path, by
// a hard link from the private name (link(2)), which refuses a name already
// there and leaves it as it is, with an error that wraps ErrMaybeBusy and
// fs.ErrExist: nobody connects to the socket with the umask's mode, and its
// mode and group are set on its own file, in a folder no other program can
// enter. Set by path at the path itself, they would land on another
// program's socket bound there in the gap, which a program that takes no
// lock and unlinks and binds at the path met in 45, 38 and 41 of 20,000
// rounds on darwin. A hard link, not a rename with no
// replace: link(2) is in every Unix's syscall, and Linux's renameat2 and
// macOS's renamex_np are in no other's. The private folder is
// <folder>/.<6 random hex>, made 0700, and holds the socket as s. A failed
// bind removes it here. Once the socket is at path, bindAt answers the
// folder with its private name still in it, which names the socket's file
// while the proof runs (own), so a proof that fails can remove the path
// while it is still that file (removeUnproved); take then removes the
// folder (settle). go-connections' NewUnixSocketWithOpts binds at the path
// itself, unlinking whatever is there first, and sets the mode by path.
func bindAt(_ context.Context, path string, given []sockets.SockOption) (net.Listener, string, error) {
	private, err := privateDir(filepath.Dir(path))
	if err != nil {
		return nil, "", fmt.Errorf("%s's private folder, where its socket is bound before it is put at the path: %w", path, err)
	}
	l, err := bindIn(filepath.Join(private, privateName), path, given)
	if err != nil {
		if rerr := removePrivate(private); rerr != nil {
			err = joined(err, fmt.Errorf("%s's private folder %s could not be removed: %w", path, private, rerr))
		}
		return nil, "", err
	}
	return l, private, nil
}

// settle removes the private folder of a socket proved its path's (own),
// and answers what held's Close runs once the socket is closed. One that
// cannot be removed while the socket is open does not fail Listen: nothing
// in it is reached, and the leftPrivate settle answers tries again once the
// socket is closed. On an NFS client the private name, unlinked while the
// socket holds it, stays in the folder as a .nfsXXXX name until the socket
// closes. When nothing was left the closer does nothing.
func settle(path, private string) io.Closer {
	if removePrivate(private) != nil {
		return leftPrivate{path: path, private: private}
	}
	return nothing{}
}

// removeUnproved removes path once the proof failed (own) and the socket
// is closed, while path is still the socket's file: a socket, and the file
// its private name, at, names, by os.SameFile. A file that is not a socket
// is left whatever names it, as one a test swaps in at the private name is.
// Only this account may enter the private folder, nothing but bindIn put a
// name there, and that name holds the file, so no other file is given its
// device and inode number while the two are looked at. Another program's socket at path, anything else there, or
// nothing, is left as it is: the proof's rule, that a socket not proved
// Listen's is never removed, stands, as the file at at is proved Listen's
// by the folder. A path that cannot be looked at, or a private name that
// cannot, is left and said.
//
// The look and the unlink are by path, as removeOwn's are, so a program
// that takes no lock and unlinks and binds at the path in the instant
// between them loses its socket. The caller holds the lock throughout.
// removed says whether path was removed, which busy reads.
func removeUnproved(path, at string) (removed bool, _ error) {
	mine, err := os.Lstat(at)
	if err != nil {
		return false, fmt.Errorf("%s is left: its socket's private name could not be looked at: %w", path, err)
	}
	now, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("%s could not be looked at, so it is left: %w", path, err)
	case now.Mode().Type() != fs.ModeSocket, !os.SameFile(now, mine):
		return false, nil
	}
	err = os.Remove(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("%s, the socket of this failed Listen, could not be removed: %w", path, err)
	}
	return true, nil
}

// unproved is a proof whose dial, or its nonce's write, failed (own): the
// failure's own error, which wraps ErrMaybeBusy only once busy says the path
// was left.
type unproved struct{ err error }

// Error is the failure's own text.
func (u unproved) Error() string { return u.err.Error() }

// Unwrap is the failure's own error, the dial's or the write's.
func (u unproved) Unwrap() error { return u.err }

// busy is own's error once take removed the path, or left it. A proof whose
// dial or nonce's write failed (unproved) wraps ErrMaybeBusy only when the
// path was left: a socket that refuses a connection, or nothing, is there,
// and it was not removed. When the path was still Listen's own socket and
// was removed, nothing refusing is there, and the error is the failure's own,
// the ctx's among them: a caller whose ctx ran out is not sent to
// ReplaceEvenIfBusy, which can cost another program its live socket. Every
// other failure of the proof is answered as own gave it.
func busy(err error, removed bool) error {
	var u unproved
	if removed || !errors.As(err, &u) {
		return err
	}
	return fmt.Errorf("%w: %w", err, ErrMaybeBusy)
}

// leftPrivate is a socket's private folder that could not be removed once
// the socket was at its path (settle), or once a failed proof closed it
// (take).
type leftPrivate struct {
	path, private string
}

// Close removes the private folder, once the socket is closed, or says why
// not; it is then left.
func (p leftPrivate) Close() error {
	if err := removePrivate(p.private); err != nil {
		return fmt.Errorf("%s's private folder %s could not be removed once its socket closed, and is left: %w", p.path, p.private, err)
	}
	return nil
}

// nothing is a Close with nothing to do: settle's closer when it removed the
// private folder.
type nothing struct{}

// Close does nothing.
func (nothing) Close() error { return nil }

// bindPrivate says the socket is bound at a private name and put at its
// path (bindAt), so its connections' own local address is the private name.
const bindPrivate = true

// privateName is the socket's name in its private folder.
const privateName = "s"

// privateRand is the random bytes in a private folder's name, said in hex.
const privateRand = 3

// privateTries is how many names privateDir tries before it gives up: a name
// another folder already has is tried again with fresh bytes.
const privateTries = 8

// bindLen is the length of the name the socket is bound at for path: its
// private name, which a Unix socket's address must hold as it holds path.
func bindLen(path string) int {
	return len(filepath.Join(filepath.Dir(path), "."+strings.Repeat("0", 2*privateRand), privateName))
}

// privateDir makes a folder in dir that only its owner may enter, 0700 (the
// umask may take more away, and nothing more is given), named . and random
// hex, and answers its path.
func privateDir(dir string) (string, error) {
	var err error
	for range privateTries {
		b := make([]byte, privateRand)
		_, _ = rand.Read(b)
		p := filepath.Join(dir, "."+hex.EncodeToString(b))
		if err = os.Mkdir(p, 0o700); !errors.Is(err, fs.ErrExist) {
			if err != nil {
				return "", err
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("%d names were taken: %w", privateTries, err)
}

// removePrivate removes the socket's private name, if it is still there, and
// its private folder. Anything else in the folder, which only its owner may
// enter, fails the folder's removal and is left.
func removePrivate(private string) error {
	err := os.Remove(filepath.Join(private, privateName))
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return joined(err, os.Remove(private))
}

// bindIn binds a socket at at, in its private folder, gives it no access and
// then what given says, by at, listens on it, and links it to path. A failure
// before the link leaves nothing at path, and the socket's private name is
// removed by bindAt; one after it cannot come, the listener being made. The
// private name is kept once the link is made (bindAt).
func bindIn(at, path string, given []sockets.SockOption) (_ net.Listener, err error) {
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd) // macOS has no SOCK_CLOEXEC.
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}
	var f *os.File
	var l net.Listener
	defer func() {
		if err == nil {
			return
		}
		if l != nil {
			err = joined(err, l.Close())
		}
		switch {
		case f != nil:
			err = joined(err, f.Close())
		case fd >= 0:
			err = joined(err, syscall.Close(fd))
		}
	}()
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: at}); err != nil {
		return nil, os.NewSyscallError("bind", err)
	}
	for _, give := range append([]sockets.SockOption{sockets.WithChmod(0)}, given...) {
		if err := give(at); err != nil {
			return nil, err
		}
	}
	if err := syscall.Listen(fd, backlog); err != nil {
		return nil, os.NewSyscallError("listen", err)
	}
	f = os.NewFile(uintptr(fd), "unix:"+path)
	fd = -1
	l, err = net.FileListener(f)
	if err != nil {
		return nil, err
	}
	// The listener holds a socket of its own, so this one closes now.
	err, f = f.Close(), nil
	if err != nil {
		return nil, err
	}
	if err := os.Link(at, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%s was taken since Listen looked, and is left as it is: %w: %w", path, ErrMaybeBusy, err)
		}
		return nil, err
	}
	return l, nil
}

// own proves that path is the socket l listens on, and answers what os.Lstat
// says of it, which Close later compares the path against (removeOwn). It
// looks at path, dials it and writes a fresh nonce, then accepts on l what is
// queued there, and looks again. The path is l's socket only if both looks
// are the same socket file and the dial is accepted on l with the nonce:
// an unlink and a bind cannot put back a socket file once unlinked, and an
// open socket holds its file, so a dial l accepts between two looks that
// match reached that file. An os.Lstat of the path alone cannot say it: a
// program that takes no lock can unlink the path and bind its own socket
// there between the bind and the look, and the look then finds that socket,
// which Close would remove. The second look
// refuses a path such a program took after the dial: Listen would answer a
// listener no client reaches. A program of the same account that renames
// another name of l's file over the path, a hard link, can put it back; the
// proof does not take that on, as such a program can as well remove the
// socket.
//
// A connection a client made before own's dial is not lost: l's queue keeps
// the order connections came in, and every one accepted ahead of own's is
// handed to the caller's first Accepts, unread (own looks at each by
// MSG_PEEK, which leaves its bytes queued). Unix's connect puts the
// connection on the listener's queue before it returns, and the nonce is
// written before own accepts, so own's connection is on l's queue, its nonce
// in, when own looks: own accepts until the queue is empty, and waits on
// nothing.
//
// A failed proof closes l and every connection it accepted, removes nothing,
// and answers an error that wraps ErrMaybeBusy when another program's socket
// is at the path, or another program unlinked it and nothing is there at a
// look. A dial that failed, the caller's ctx having ended among the ways, or
// a nonce's write, answers the failure's own error (unproved). A file there,
// not a socket, is refused as one, as it was. take then removes the path
// while it is still l's own file (removeUnproved), and only then, and an
// unproved error wraps ErrMaybeBusy only if the path was left (busy).
// early and proved are a test's alone, run only under testing.Testing():
// early before the first look, proved once the dial is accepted and before
// the second.
func own(ctx context.Context, path string, l net.Listener, early, proved func(path string)) (_ net.Listener, _ fs.FileInfo, err error) {
	var ahead []net.Conn
	defer func() {
		if err != nil {
			for _, c := range ahead {
				err = joined(err, c.Close())
			}
			err = joined(err, l.Close())
		}
	}()
	ul, ok := l.(*net.UnixListener)
	if !ok {
		return nil, nil, fmt.Errorf("%s is bound as a %T, not a Unix socket's listener, so it cannot be proved the socket's", path, l)
	}
	if early != nil && testing.Testing() {
		early(path)
	}
	first, err := socketAt(path)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, nonceLen)
	_, _ = rand.Read(nonce)
	c, err := (&net.Dialer{}).DialContext(ctx, unixNet, path)
	if err != nil {
		return nil, nil, unproved{fmt.Errorf("%s could not be dialed to prove it the socket bound: %w", path, err)}
	}
	deadline, _ := ctx.Deadline()
	err = c.SetWriteDeadline(deadline)
	if err == nil {
		_, err = c.Write(nonce)
	}
	if err != nil {
		return nil, nil, joined(unproved{fmt.Errorf("%s was dialed to prove it the socket bound, and the nonce could not be written: %w", path, err)}, c.Close())
	}
	ahead, mine, err := drain(ul, path, nonce)
	err = joined(err, c.Close())
	if err != nil {
		return nil, nil, err
	}
	if proved != nil && testing.Testing() {
		proved(path)
	}
	second, err := socketAt(path)
	switch {
	case err != nil:
		return nil, nil, err
	case !mine:
		return nil, nil, fmt.Errorf("%s is not the socket bound: a dial to it was not accepted there, so it is another program's and is left: %w", path, ErrMaybeBusy)
	case !os.SameFile(first, second):
		return nil, nil, fmt.Errorf("%s was swapped while it was proved the socket bound, so it is left: %w", path, ErrMaybeBusy)
	}
	if len(ahead) == 0 {
		return ul, first, nil
	}
	return &queued{Listener: ul, early: ahead}, first, nil
}

// socketAt is what os.Lstat says of path, once bound: a socket, or why not. A
// path gone is another program's unlink, as a path swapped is, and wraps
// ErrMaybeBusy; the lstat error is said, not wrapped, so a caller does not
// read it as a folder that is not there.
func socketAt(path string) (fs.FileInfo, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		//nolint:errorlint // said, not wrapped: fs.ErrNotExist here is not a folder that is not there
		return nil, fmt.Errorf("%s was gone when it was looked at to prove it the socket bound: another program unlinked it, and nothing is removed (%v): %w", path, err, ErrMaybeBusy)
	}
	if err == nil && fi.Mode().Type() != fs.ModeSocket {
		err = fmt.Errorf("%s is a %s once bound, not the socket: it is left as it is", path, kind(fi.Mode()))
	}
	if err != nil {
		return nil, err
	}
	return fi, nil
}

// drain accepts on l until its queue is empty, and says whether one of the
// connections carried nonce: early is every other connection accepted, in the
// order it came, its bytes left unread. The nonce's connection is closed.
func drain(l *net.UnixListener, path string, nonce []byte) (early []net.Conn, mine bool, err error) {
	rc, err := l.SyscallConn()
	if err != nil {
		return nil, false, err
	}
	for !mine {
		nfd := -1
		var aerr error
		// A listener's raw conn has Control alone (its Read is EINVAL), and
		// the listener's socket is nonblocking, so the accept does not wait.
		if err := rc.Control(func(fd uintptr) {
			syscall.ForkLock.RLock()
			nfd, _, aerr = syscall.Accept(int(fd))
			if aerr == nil {
				syscall.CloseOnExec(nfd)
			}
			syscall.ForkLock.RUnlock()
		}); err != nil {
			return early, false, err
		}
		switch {
		case errors.Is(aerr, syscall.EAGAIN), errors.Is(aerr, syscall.EWOULDBLOCK):
			return early, false, nil
		case errors.Is(aerr, syscall.EINTR), errors.Is(aerr, syscall.ECONNABORTED):
			continue
		case aerr != nil:
			return early, false, os.NewSyscallError("accept", aerr)
		}
		if mine = carries(nfd, nonce); mine {
			if err := syscall.Close(nfd); err != nil {
				return early, true, os.NewSyscallError("close", err)
			}
			continue
		}
		f := os.NewFile(uintptr(nfd), "unix:"+path)
		c, err := net.FileConn(f)
		err = joined(err, f.Close())
		if err != nil {
			if c != nil {
				err = joined(err, c.Close())
			}
			return early, false, err
		}
		early = append(early, c)
	}
	return early, true, nil
}

// carries says whether what is queued on fd, unread, is nonce and nothing
// more. It looks by MSG_PEEK, so a client's bytes stay queued for the caller.
func carries(fd int, nonce []byte) bool {
	if syscall.SetNonblock(fd, true) != nil {
		return false
	}
	b := make([]byte, len(nonce)+1)
	n, _, err := syscall.Recvfrom(fd, b, syscall.MSG_PEEK)
	return err == nil && n == len(nonce) && subtle.ConstantTimeCompare(b[:n], nonce) == 1
}

// queued is a listener whose first Accepts answer the connections own
// accepted ahead of its proof, in the order they came.
type queued struct {
	net.Listener
	mu    sync.Mutex
	early []net.Conn
}

// Accept answers the next connection own accepted, and then the listener's.
func (q *queued) Accept() (net.Conn, error) {
	q.mu.Lock()
	if len(q.early) > 0 {
		c := q.early[0]
		q.early = q.early[1:]
		q.mu.Unlock()
		return c, nil
	}
	q.mu.Unlock()
	return q.Listener.Accept()
}

// Close closes the connections no Accept took, and the listener.
func (q *queued) Close() error {
	q.mu.Lock()
	early := q.early
	q.early = nil
	q.mu.Unlock()
	var err error
	for _, c := range early {
		err = joined(err, c.Close())
	}
	return joined(err, q.Listener.Close())
}
