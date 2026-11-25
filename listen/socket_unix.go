//go:build unix

package listen

import (
	"context"
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
	"github.com/gofrs/flock"
)

// sunPath is the most bytes a Unix socket's path may have on this platform:
// its sun_path, as the kernel's sockaddr_un has it, less the NUL that ends it.
// Linux's is 108, so 107; macOS's and the BSDs' are 104, so 103.
const sunPath = len(syscall.RawSockaddrUnix{}.Path) - 1

// unixNet is a Unix socket's network, as net names it.
const unixNet = "unix"

// listen is Listen's options as given, for a Unix socket.
type listen struct {
	mode os.FileMode
	// modeSet says SocketMode was given; Windows refuses it there.
	modeSet bool
	group   string
	// evenIfBusy says ReplaceEvenIfBusy was given: rule 4 replaces a socket
	// that refuses a connection.
	evenIfBusy bool
	// probe is set by a test alone, and Listen runs it only under
	// testing.Testing(): last of what the socket is given before it listens.
	probe func(path string) error
	// earlyProbe is set by a test alone, and own runs it only under
	// testing.Testing(): once the socket listens, before the proof's first
	// look and its dial.
	earlyProbe func(path string)
	// provedProbe is set by a test alone, and own runs it only under
	// testing.Testing(): once the proof's dial is accepted and before its
	// second look.
	provedProbe func(path string)
	// lockProbe is set by a test alone, and Listen runs it only under
	// testing.Testing(): before the lock file is opened, and once it is locked.
	lockProbe func(stage string)
	// bindProbe is set by a test alone, and Listen runs it only under
	// testing.Testing(): once the path is looked at, and what rule 4 replaces
	// removed, and before the socket is bound; replaced says rule 4 removed a
	// socket.
	bindProbe func(replaced bool)
}

// listenLocal is a Unix socket of the program's own at the path addr, by
// Listen's rules, or why not. A name starting \, as a pipe's does, is
// Windows' alone: here it is refused with ErrPipeName, and nothing is made.
func listenLocal(ctx context.Context, addr string, o listen) (net.Listener, error) {
	if strings.HasPrefix(addr, `\`) {
		return nil, fmt.Errorf("%q: a pipe's name is Windows' alone, and here Listen takes a socket's path: %w", addr, ErrPipeName)
	}
	path, err := fits(addr)
	if err != nil {
		return nil, err
	}
	given, err := access(o)
	if err != nil {
		return nil, err
	}
	if o.probe != nil && testing.Testing() {
		given = append(given, o.probe)
	}
	lock, err := lockAt(path+".lock", o.lockProbe)
	if err != nil {
		return nil, err
	}
	l, bound, left, err := take(ctx, path, given, o)
	if err != nil {
		return nil, joined(err, lock.Unlock())
	}
	return &held{Listener: l, remove: stillThere{path: path, bound: bound, lock: lock}, after: left, lock: lock, addr: &net.UnixAddr{Name: path, Net: unixNet}}, nil
}

// fits is addr made absolute, against the folder the program is in, or why a
// Unix socket's address cannot hold it here: its own length, or its private
// name's. Listen and Local both ask it, so Local refuses what Listen would.
func fits(addr string) (string, error) {
	path, err := filepath.Abs(addr)
	if err != nil {
		return "", fmt.Errorf("%s made absolute: %w", addr, err)
	}
	if n := len(path); n > sunPath {
		return "", fmt.Errorf("%s is %d bytes, and a Unix socket's path holds at most %d here: choose a shorter one", path, n, sunPath)
	}
	if n := bindLen(path); n > sunPath {
		return "", fmt.Errorf("%s's socket is bound first at a name in a private folder beside it, %d bytes, and a Unix socket's path holds at most %d here: choose a shorter folder", path, n, sunPath)
	}
	return path, nil
}

// localAt is Local's address on Unix: name in folder, made absolute, if a
// Unix socket's address holds it and its private name here.
func localAt(folder, name string) (string, error) { return fits(filepath.Join(folder, name)) }

// lockAt takes the lock file at p, rule 1 of Listen, opened only as a plain
// file of that name (lockFlag says how on each system), or says why not.
// probe is a test's alone, run only under testing.Testing(): before the open,
// and once the lock is held.
func lockAt(p string, probe func(stage string)) (*flock.Flock, error) {
	step := func(stage string) {
		if probe != nil && testing.Testing() {
			probe(stage)
		}
	}
	if err := plainOrNone(p); err != nil {
		return nil, err
	}
	step("open")
	lock := flock.New(p, flock.SetFlag(lockFlag), flock.SetPermissions(0o600))
	ok, err := lock.TryLock()
	if err != nil {
		// A link put there since the look above fails the open: say it as one.
		if perr := plainOrNone(p); perr != nil {
			return nil, perr
		}
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	if !ok {
		return nil, fmt.Errorf("%s is held: %w", p, ErrHeld)
	}
	step("locked")
	// What was opened and locked must be the plain file at p now: a plain
	// file, and not one renamed away since the open.
	got, err := lock.Stat()
	if err == nil && !got.Mode().IsRegular() {
		err = fmt.Errorf("%s is a %s, not a plain file: %w", p, kind(got.Mode()), ErrLockNotAFile)
	}
	if err == nil {
		switch at, lerr := os.Lstat(p); {
		case lerr != nil:
			err = fmt.Errorf("%s, once locked: %w", p, lerr)
		case !os.SameFile(got, at):
			err = fmt.Errorf("%s was replaced while it was locked: %w", p, ErrLockNotAFile)
		}
	}
	if err != nil {
		return nil, joined(err, lock.Unlock())
	}
	return lock, nil
}

// plainOrNone is nil when p is a plain file or is not there, and otherwise
// why it is not a lock file.
func plainOrNone(p string) error {
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !fi.Mode().IsRegular():
		return fmt.Errorf("%s is a %s, not a plain file: it is left as it is: %w", p, kind(fi.Mode()), ErrLockNotAFile)
	}
	return nil
}

// take is rules 2 to 5 of Listen, with the lock held: the socket at path,
// bound with the options given, and what os.Lstat says of it once bound, or
// why not. That look is proved the socket's (own): a path another
// program's socket took since the bind is refused and left. o.evenIfBusy says a
// socket that refuses the connection is replaced (rule 4), which only
// ReplaceEvenIfBusy says. That socket alone is removed, while the path is
// still it; nothing else is, so a name taken since take looked is refused,
// by the link, and is left. o.bindProbe is a test's alone, run only under
// testing.Testing(): once take has looked, and removed what rule 4 replaces,
// and before it binds; replaced says rule 4 removed a socket. o.earlyProbe and
// o.provedProbe are a test's alone, which own runs before the dial that
// proves the look and once it is accepted. The listener does not remove the
// path when it closes: held's Close does, by removeOwn. A proof that fails
// removes the path while it is still the socket's own file, by its private
// name (removeUnproved), and nothing else; its error wraps ErrMaybeBusy as
// busy says. left is what is left to remove
// once the socket is closed, which held's Close runs after it: a private
// folder that could not be removed (settle).
func take(ctx context.Context, path string, given []sockets.SockOption, o listen) (_ net.Listener, _ fs.FileInfo, left io.Closer, _ error) {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, nil, nil, err
	case fi.Mode().Type() != fs.ModeSocket:
		return nil, nil, nil, fmt.Errorf("%s is a %s, not a socket: it is left as it is", path, kind(fi.Mode()))
	default:
		var d net.Dialer
		c, err := d.DialContext(ctx, unixNet, path)
		switch {
		case err == nil:
			return nil, nil, nil, joined(fmt.Errorf("%s answers: %w", path, ErrHeld), c.Close())
		case full(err):
			return nil, nil, nil, fmt.Errorf("%s answers that its queue is full: %w", path, ErrHeld)
		case !refused(err):
			return nil, nil, nil, fmt.Errorf("%s could not be asked whether a program listens there, so it is left: %w", path, err)
		case !o.evenIfBusy:
			return nil, nil, nil, fmt.Errorf("%s refuses a connection: %w; give ReplaceEvenIfBusy if no program that takes no lock listens there", path, ErrMaybeBusy)
		}
		if err := noneBound(path); err != nil {
			return nil, nil, nil, err
		}
		if err := removeIfStill(path, fi); err != nil {
			return nil, nil, nil, fmt.Errorf("%s, a run before's socket, removed: %w", path, err)
		}
	}
	if o.bindProbe != nil && testing.Testing() {
		o.bindProbe(fi != nil)
	}
	l, private, err := bindAt(ctx, path, given)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s could not be bound, and what is there is left as it is: %w", path, err)
	}
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	l, bound, err := own(ctx, path, l, o.earlyProbe, o.provedProbe)
	if err != nil {
		// own closed the socket. The path is removed while it is still its
		// file, which the private name still names, and then the private
		// folder, so a Listen that fails for its own reason, its ctx among
		// them, leaves no socket of its own at the path. A failed dial or
		// nonce's write says ErrMaybeBusy only if the path is left (busy).
		removed, rerr := removeUnproved(path, filepath.Join(private, privateName))
		err = joined(busy(err, removed), rerr)
		return nil, nil, nil, joined(err, leftPrivate{path: path, private: private}.Close())
	}
	return l, bound, settle(path, private), nil
}

// full says whether a connection failed because the socket's queue is full,
// as Linux says it (EAGAIN): a program listens there, busy. The other
// systems say it as a refusal, which refused cannot tell apart.
func full(err error) bool { return errors.Is(err, syscall.EAGAIN) }

// kind is a file's type, said for a refusal.
func kind(m fs.FileMode) string {
	switch {
	case m.IsDir():
		return "folder"
	case m&fs.ModeSymlink != 0:
		return "symbolic link"
	case m.IsRegular():
		return "file"
	}
	return "file of type " + m.Type().String()
}

// held is a socket of the program's own, what removes it, and the lock it
// holds for its life.
type held struct {
	net.Listener
	// remove removes the socket bound and nothing else, while the socket is
	// still open, which removeOwn needs.
	remove stillThere
	// after is what settle left to remove once the socket is closed: a
	// private folder that could not be removed while it was open.
	after io.Closer
	lock  *flock.Flock
	// addr is the path, which the socket is at: it was bound at a private
	// name (bindAt), which its own address says.
	addr net.Addr
	once sync.Once
	err  error
}

// Accept answers the next connection. Where the socket was bound at a
// private name and put at its path (bindPrivate), a connection's own local
// address says the private name, so it is answered as a pathConn, whose
// LocalAddr is the path.
func (h *held) Accept() (net.Conn, error) {
	c, err := h.Listener.Accept()
	if uc, ok := c.(*net.UnixConn); ok && err == nil && bindPrivate {
		return &pathConn{UnixConn: uc, local: h.addr}, nil
	}
	return c, err
}

// pathConn is a connection on a socket bound at a private name and put at
// its path (bindAt): its LocalAddr is the path. It has every other method of
// the *net.UnixConn it holds, SyscallConn among them, which a check of the
// peer reads (peer), and ReadMsgUnix; it is not itself a *net.UnixConn, and
// UnixConn is the one it holds.
type pathConn struct {
	*net.UnixConn
	local net.Addr
}

// LocalAddr is the socket's path.
func (c *pathConn) LocalAddr() net.Addr { return c.local }

// Addr is the path the socket is at.
func (h *held) Addr() net.Addr { return h.addr }

// Close removes the socket and closes it, and then lets the lock go. A path
// that no longer names the socket bound, or a lock file that is no longer the
// one held, is left, and Close says so with ErrSocketLeft. A private folder
// Listen could not remove is removed once the socket is closed, or Close
// says why not and leaves it. A second Close says what the first said.
func (h *held) Close() error {
	h.once.Do(func() {
		err := joined(h.remove.Close(), h.Listener.Close())
		err = joined(err, h.after.Close())
		h.err = joined(err, h.lock.Unlock())
	})
	return h.err
}

// stillThere removes the socket bound at path, while its caller holds the
// socket open (removeOwn).
type stillThere struct {
	path  string
	bound fs.FileInfo
	lock  *flock.Flock
}

// Close is removeOwn(path, bound, lock).
func (s stillThere) Close() error { return removeOwn(s.path, s.bound, s.lock) }

// removeOwn removes path only while both hold: lock is still held, and still
// the lock file at its path, and path is still the socket bound, by
// os.SameFile against what os.Lstat said of it once bound, a look own proved
// the socket's. The caller holds the socket open meanwhile,
// and an open socket holds the file it is bound to, so no other file is given
// that file's device and inode number. Otherwise the path is left, and
// removeOwn says so with ErrSocketLeft; nothing at the path is nothing to
// remove. The look and the unlink are by path, so a program that takes no
// lock and unlinks and binds at the path in the instant between them loses
// its socket: no kernel lets an unlink say which file it removes.
//
// A socket's own fd cannot say which file it is bound to: fstat on it says
// the socket's inode (sockfs on Linux), not the file's.
func removeOwn(path string, bound fs.FileInfo, lock *flock.Flock) error {
	if err := stillLocked(lock); err != nil {
		return fmt.Errorf("%s is left: %w", path, err)
	}
	now, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("%s could not be looked at, so it is left: %w: %w", path, err, ErrSocketLeft)
	case !os.SameFile(now, bound):
		return fmt.Errorf("%s is no longer the socket this listener bound: %w", path, ErrSocketLeft)
	}
	return os.Remove(path)
}

// stillLocked is nil while lock's file is still the plain file at its path,
// and otherwise says why not, with ErrSocketLeft: one that removed it under a
// live listener may have made another and locked that.
func stillLocked(lock *flock.Flock) error {
	// With no lock held, lock.Stat looks at the path, which would pass.
	if !lock.Locked() {
		return fmt.Errorf("%s is not locked by this listener: %w", lock.Path(), ErrSocketLeft)
	}
	got, err := lock.Stat()
	if err != nil {
		return fmt.Errorf("the lock file held could not be looked at: %w: %w", err, ErrSocketLeft)
	}
	at, err := os.Lstat(lock.Path())
	switch {
	case err != nil:
		return fmt.Errorf("%s is no longer the lock file held: %w: %w", lock.Path(), err, ErrSocketLeft)
	case !os.SameFile(got, at):
		return fmt.Errorf("%s is no longer the lock file held: %w", lock.Path(), ErrSocketLeft)
	}
	return nil
}

// removeIfStill is rule 4's removal: path while it is still the file was, by
// os.SameFile. Something else there now, or nothing, is left as it is. It is
// a look and then an unlink by path, with nothing holding the file: a file put
// at the path between them is removed, and so is one given was's device and
// inode number once was is gone, as ext4 and overlayfs give a freed number
// again. So it runs only with ReplaceEvenIfBusy.
func removeIfStill(path string, was fs.FileInfo) error {
	now, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(now, was) {
		return nil
	}
	return os.Remove(path)
}
