# listen

`listen` answers over HTTP and costs nothing between visits: the sockets systemd or launchd passes (`Activated`), a socket of its own for a container or a test under a lock file (`Listen`), serve until idle with `Hold` (`Serve`, `NewIdle`, `Options`), each listener's handler (`Same`), and what each connection adds to its requests (`Options.ConnContext`).

A program imports `github.com/birdtable/go-socket/listen`.

- **What it imports**: `github.com/coreos/go-systemd/v22` (`activation`),
  `github.com/docker/go-connections` (`sockets`), `github.com/gofrs/flock`, on macOS
  `github.com/bored-engineer/go-launchd` for launchd's sockets, and on Windows
  `github.com/Microsoft/go-winio` for its named pipe, and no module of ours. The root
  [layers.yaml](../layers.yaml) says so.

`go doc github.com/birdtable/go-socket/listen` has the rest; every exported name is documented.

## How it works

Built on `net/http` and four modules: go-systemd's `activation` for the sockets systemd passes,
go-launchd's `Files` for the ones launchd passes, go-connections' socket options, which give a
Unix socket its mode before it listens, and flock's lock file. On Windows it is built on
go-winio's named pipe.

It adds what they leave out: the five rules that tell a socket a run before left from one a live
program holds, a bind that removes nothing, a `Close` that removes only the socket it bound, and
the idle exit. On Windows it adds the pipe's access list, a check of its name, and a pipe already
there refused as held.

The program configures the path, pipe name or address, the socket's mode (0600 unless it gives
another) and group, each listener's handler, and what a connection adds to its requests. It
configures every span too: `ctx`'s deadline, `Grace`, `ReadHeaderTimeout`, `KeepAlive`, and
`NewIdle`'s `after` and `linger`, none with a default.

## A listener of the program's own

`Listen(ctx, addr)` takes a Unix socket's path on Unix and a named pipe's name on Windows, and
nothing gives either a default: the caller names it. A pipe's name off Windows, and a path on
Windows, is refused with `ErrPipeName`, and nothing is made.

`Local(folder, name)` gives that address for a name the program picks, so it needn't choose a
form by system. On Unix it's `name` in `folder`, made absolute, and refused with `Listen`'s own
error when it's too long for a socket's address, its private name included. On Windows it's
`\\.\pipe\<name>-<h>`, where `<h>` is the first 16 hex digits of the SHA-256 of the absolute
folder, so two folders never share a pipe. The one string is what `Listen` takes and what a
client is told, `SSH_AUTH_SOCK` among them. `name` is one plain name: empty (`ErrNoAddress`),
`.`, `..`, a `/` or `\`, a NUL, and on Windows a trailing dot or space are refused, quoting it.
`Local` makes nothing and doesn't look at the folder.

## A Unix socket

On Unix, `Listen(ctx, path)` holds the lock file `<path>.lock` for the listener's life, and decides
in this order:

1. The lock is held by another program: refused, with `ErrHeld`.
2. Something at the path is not a socket: refused, and left as it is.
3. The socket at the path answers a connection, or says its queue is full (`EAGAIN`, as Linux
   says it): refused, with `ErrHeld`.
4. It refuses the connection, as one nothing listens on does (`ECONNREFUSED`), and the lock is
   ours: refused with `ErrMaybeBusy` and left, on every system and kernel, unless the caller
   gave `ReplaceEvenIfBusy`. With it, the socket is replaced (on Linux and darwin, once a
   datagram connection says no socket is bound to it still; a socket bound there and not yet
   listening, or on darwin listening with its queue full, is refused with `ErrHeld` and left).
5. The connection fails any other way: refused, and the socket is left.

The lock is let go on every refusal, and by `Close`. The lock file itself stays: a program that
removes one can take it while another still holds it.

- **What the lock covers.** Programs that listen through `Listen`, while `<path>.lock` keeps its
  name. A program that removes the lock file under a live listener leaves that listener rules 3
  to 5.
- **A full queue.** Linux answers a connection to a socket whose queue is full with `EAGAIN`,
  which rule 3 counts as a live listener. macOS and the BSDs refuse it as they refuse a
  connection to a socket nothing listens on, so it reaches rule 4. On darwin, with
  `ReplaceEvenIfBusy`, rule 4's datagram connection still finds it and leaves it.
- **Replacing a socket is the caller's call, on every system.** Rule 4 cannot tell a socket a
  run before left from a live one of a program that takes no lock. Its removal is a look and
  then an unlink by path, and nothing holds the name between them. So a program that takes no
  lock and unlinks and binds at the path in that gap (go-connections' `NewUnixSocketWithOpts`
  unlinks before it binds) loses its live socket. Where the filesystem gives a freed inode
  number again (ext4, overlayfs), the new socket can even look like the one `Listen` looked at.
  No kernel closes that gap, so `Listen` replaces a socket that refuses a connection only with
  `ReplaceEvenIfBusy`, the option that names the risk. Without it the socket is refused with
  `ErrMaybeBusy`, left as it is, and the lock let go. Give it where no program that takes no lock
  listens at the path: a program that listens through `Listen` holds the lock, and rule 1 refuses
  its path before its socket is asked. A socket a run before left (a crash, a kill) needs the
  option, on Linux as on darwin.
- **On Linux and darwin, with the option, a starting socket is still left.** A program that takes
  no lock and has bound its socket but not yet listened on it (go-connections holds a socket so
  while it gives it its mode and group) refuses a stream connection as a run before's socket
  does. Its socket is still bound to the file, though, and a datagram connection finds it: Linux
  and darwin answer `EPROTOTYPE` where a stream socket is bound, `ECONNREFUSED` where none is
  (both measured). So before rule 4 removes anything there, `Listen` asks that
  way. On darwin that also leaves a live socket whose queue is full, which refuses a stream
  connection as a run before's does. The BSDs ask nothing more, since what they answer is
  unmeasured (the tests have not run on FreeBSD): there the option replaces every socket that refuses. A socket bound there is refused with
  `ErrHeld`, and any answer but `ECONNREFUSED` leaves the file as it is. First, `Listen` renames
  the path onto itself, which changes nothing. From Linux 5.14 that rename waits on the folder
  lock the bind holds from making the socket's file until the socket is bound to it, so a bind
  under way is asked once it has finished. Before 5.14 the bind lets the folder go between the
  two, and a bind under way may be missed. On darwin no lock of the bind is known that the
  rename waits on, so a bind under way may be missed there too. A rename that fails leaves the
  file as it is. `bound_dgram.go` names the Linux source lines this relies on. This ask narrows what the
  option replaces. It guarantees nothing about a program that binds after it.
- **A name taken since `Listen` looked is left.** `Listen` removes only the socket rule 4
  replaces, and only while the path is still it, then binds without removing anything first. A
  program that takes no lock and binds at the path after that removal (or after `Listen`'s look,
  where nothing was there) keeps its socket: `Listen`'s bind is refused, nothing is removed, and
  the lock is let go. One that binds between rule 4's look and its removal does not: that is the
  gap `ReplaceEvenIfBusy` names.
- **The lock file is a plain file of its own name.** It is opened with `O_NOFOLLOW`. A symbolic
  link at `<path>.lock`, a folder, or anything else that is not a plain file is refused with
  `ErrLockNotAFile`, before the open and again once locked, and left as it is. Nothing is made
  where a link points, and no socket is made.
- **`Close` removes only the socket it bound, with no option.** A clean restart then finds nothing
  at the path. `Listen` takes the socket's `os.Lstat` once bound and turns its unlink-on-close off.
  `Close` removes the path before it closes the socket and lets the lock go, and only while both
  hold: `<path>.lock` is still the lock file it holds, and the path is still the socket bound, by
  `os.SameFile` against that `os.Lstat`. An open socket holds its file, so no other file is given
  its device and inode number meanwhile. Otherwise the path is left, a socket another program put
  there since or a file, and `Close` says so with `ErrSocketLeft`. A socket's own fd cannot say
  which file it is bound to: `fstat` on it says the socket's inode, not the file's.
- **`Listen` proves that look the socket's before it answers.** Once the socket listens, `Listen`
  looks at the path, dials it and writes a fresh nonce, accepts on its own socket what is queued
  there, and looks again. The look is the socket's only if both looks are the same socket file and
  the nonce came in on its own socket: an unlink and a bind cannot put back a socket file once
  unlinked. A program of the same account that renames a hard link of it back over the path can,
  and the proof does not take that on: such a program can as well remove the socket. A path
  another program that takes no lock unlinked and bound since `Listen` bound fails the proof:
  `Listen` closes its socket, leaves the path, and fails with `ErrMaybeBusy`. When a proof's dial
  fails, the ctx having ended among the ways, `Listen` removes the path while it is still its own
  socket's file, which the socket's private name, in a folder only this account may enter, still
  names: a `Listen` that fails for its own reason leaves no socket at the path, and fails with the
  dial's own error, which wraps the ctx's, and not with `ErrMaybeBusy`, as nothing refusing is
  left there. Another program's socket there is left, and that failure wraps `ErrMaybeBusy`.
  A client that connected before the proof's dial is not lost:
  its connection is the first `Accept`, with what it wrote unread. A failure before the socket
  is at its path removes nothing there, and the error is the failure's own, with no
  `ErrSocketLeft`.
  A path another program took after the proof's dial is refused by its second look, as `Listen`
  would answer a listener no client reaches. A path another program unlinked, with nothing there
  when either look is taken, is refused with `ErrMaybeBusy` too, and nothing is removed.
  A group that is not there is refused before anything is made, and so is a `SocketMode` its
  owner may not write (0200), with an error that wraps `fs.ErrInvalid`: the proof's dial needs it.
- **The window `Close` leaves.** `Close`, and a proof that fails, are the two places `Listen`
  removes without `ReplaceEvenIfBusy`. In each, the look and the unlink are by path, so a program that takes no lock and
  unlinks and binds at the path in the instant between them loses its socket. No kernel lets an
  unlink say which file it removes. A test that raced `Close` so on darwin-arm64 met it in 5, 0
  and 0 of 20,000 rounds, and in none in four more runs with longer delays.

- **The socket's folder is the caller's to check.** `Listen` does not look at it: a socket in a
  folder another account may write can be swapped under it, whatever its own mode.
- **The socket** is bound with no access at all in a private folder beside the path,
  `<folder>/.<6 random hex>`, made 0700, given its mode (`SocketMode`, 0600 by default) and group
  (`SocketGroup`) there by its private name, and listens. Only then is it put at the path, by a
  hard link (`link(2)`), which refuses a name already there and leaves it, with an error that
  wraps `ErrMaybeBusy` and `fs.ErrExist`, and its private name
  and folder are removed, on every way out. So nobody connects to it with the umask's mode, and
  its mode and group are set on its own file alone. Set by path at the path, they would land on
  another program's socket bound there in the gap: a test that raced a bind so met that in 45, 38
  and 41 of 20,000 rounds on darwin. A hard link, not a rename that does not
  replace: `link(2)` is in every Unix's `syscall`, and Linux's `renameat2` and macOS's
  `renamex_np` are in no other's. The socket's folder must take hard links (FAT, exFAT and some
  network shares do not), and `Listen` must be let make a folder there, link and remove names
  in it, and remove it: under an AppArmor profile or an SELinux policy that grants only the
  socket and its lock file, `Listen` fails, and nothing is left at the path. A private folder
  that cannot be removed once the socket is at its path does not fail `Listen`: it is 0700,
  nothing in it is reached, and `Close` tries again once the socket is closed, and says so if
  that fails too. On an NFS client the private name, unlinked while the socket holds it, stays
  in the folder as a `.nfsXXXX` name until then. A process killed while it starts may leave its
  private folder, which no later start removes.
- **A relative path** is made absolute when `Listen` is called, against the folder the program
  is in then. The lock file, the socket and `Close` all use that path, wherever the program moves
  after.
- **An empty address** is refused with `ErrNoAddress`, and nothing is made: it is neither a path
  nor a TCP address.
- **Its path's length**, made absolute, is held to what a Unix socket's address takes on the system: 107 bytes
  on Linux, 103 on macOS and the BSDs, the kernel's `sun_path` less its NUL
  (`syscall.RawSockaddrUnix`). A longer path is refused before anything is made, the lock file
  included, and the error names the path and the limit. The socket's private name must fit too,
  its folder and 10 bytes more, and a path whose private name does not is refused the same way,
  naming the private folder.
- **The listener's `Name`** is the path `Listen` was given, its `Addr` the absolute path the
  socket is at, and `Local` is true. A connection's `LocalAddr` is that path too: the
  connection `Accept` answers is not itself a `*net.UnixConn`, whose own address is the private
  name, but has every method of one, `SyscallConn` among them. That private name, gone once the
  socket is at its path, is still the socket's own address: a client's `getpeername(2)` and
  `RemoteAddr`, `ss -xl` and `/proc/net/unix` say it, not the path. An
  address that is not local (it does not start `/`, `.` or `\`, and is not absolute) is a TCP
  address, with no lock, and `Local` is false.

## A named pipe, on Windows

On Windows, `Listen(ctx, name)` serves a go-winio named pipe in place of a Unix socket.

- **The name** is `\\.\pipe\` and then a name with no `\` or `/` in it that does not end in a dot
  or a space, which Win32 drops. Anything else starting `\`, `.` or a drive (a path, another
  machine's pipe, `\\?\`) is refused with `ErrPipeName`, the name quoted, and nothing is made.
- **The owner and access list are set as the pipe is made**, so it is never open to anyone else
  in between: the account the program runs as owns it (an elevated administrator's pipe too, not
  Administrators) and may read and write it and make its next server end; SYSTEM may do
  anything; nothing is inherited (`O:<sid>D:P(A;;GA;;;SY)(A;;FRFW;;;<sid>)`). The kernel refuses
  everyone else at the dial.
- **Elevation is no line**, as it is none for a Unix socket's uid: a process of the same account,
  elevated or not, may dial the pipe, make its next server end and, as its owner, rewrite its
  list.
- **A pipe already at the name**, made by anybody, is refused with `ErrHeld` and never joined: the
  first server end is made with `FILE_CREATE`, as `FILE_FLAG_FIRST_PIPE_INSTANCE` asks, and
  Windows answers it there with `ERROR_ACCESS_DENIED` (never `ERROR_ALREADY_EXISTS`), or with
  `ERROR_PIPE_BUSY` where the pipe's holder bounded its instances and made them all (.NET's
  `NamedPipeServerStream` makes one by default). The `ErrHeld` error wraps either.
- **A token that may make no pipe**, in an app container or under a restricted token, meets
  `ERROR_ACCESS_DENIED` at a free name too. `Listen` then asks, as the process and connecting to
  nothing, whether a pipe is at the name (`FindFirstFile` on its full name): where Windows answers
  that none is, the error wraps `fs.ErrPermission` and not `ErrHeld`. Any other answer, and a name
  holding a character Windows takes as a wildcard (`* ? < > "`), stays `ErrHeld`, so a held name
  is never without `ErrHeld`. The `fs.ErrPermission` answer reads the name at that moment and is
  no proof of a missing right: a pipe another program makes and closes between the create and the
  look gives it too. An `ErrHeld` error for `ERROR_ACCESS_DENIED` matches `fs.ErrPermission` as
  well, since it wraps that `Errno`, so test `ErrHeld` first.
- **Clients on other machines** are refused: go-winio makes every pipe with
  `FILE_PIPE_REJECT_REMOTE_CLIENTS`.
- **Who connected** is peer's to say: `peer.PeerOf` names each connection's account before a byte
  is read, for a client that dials with `peer.DialPipe`, and `peer.Listener` keeps the accounts a
  program trusts.
- **While a server end of the pipe is open, a `Listen` at its name is `ErrHeld`**: the listener's,
  or an accepted connection's, which holds the name past the listener's `Close` until it closes
  too. The process's exit closes them all. Don't count on a client that keeps its end to hold the
  name: on Windows 11 (build 26200) the name was seen free once the last server end closed, and a
  restarted program's `Listen` took it, while that client's end reached nothing of the new pipe.
  A Windows that kept the name would refuse that `Listen` with `ErrHeld` until the client closed.
  There is no lock file, no rule 2 to 5, no `ErrMaybeBusy` and nothing for `Close` to remove.
- **The Unix socket's options are refused, not ignored.** `SocketMode`, `SocketGroup` and
  `ReplaceEvenIfBusy` each give an error that names the option and wraps
  `errors.ErrUnsupported`, and nothing is made.

## Every span is the program's

`listen` waits no span of its own: each is the caller's, and one at zero is refused by its name
before anything is made or served.

| span | given by | refused |
|---|---|---|
| how long `Listen` waits on the one connection it makes to a socket at its path, and on a TCP listen | `ctx`'s deadline | a `ctx` with no deadline, naming the deadline, and nothing is made |
| how long the requests in flight get when `ctx` ends | `Options.Grace` | zero or below, by its name |
| how long a request's headers may take | `Options.ReadHeaderTimeout` | zero or below, by its name |
| how long a connection with no request in it stays open | `Options.KeepAlive` | zero or below, by its name |
| how long a process stays with nothing held | `NewIdle`'s `after` | zero or below, by its name, when `Serve` is given the `Idle` |
| how long the connections still open when an `Idle` is over get, before they are closed | `NewIdle`'s `linger` | zero or below, by its name, when `Serve` is given the `Idle` |

`Serve` names every span it refuses in one error, and leaves the listeners open, the caller's to
close. The socket's mode is not a span: 0600 stays the default, the safe one, and `SocketMode`
gives another.

## A socket systemd or launchd passes

On Linux, `Activated` takes the sockets systemd passed, each named by its
`FileDescriptorName=` (or `LISTEN_FD_<n>`), and clears the variables that passed them, so a
program it starts does not take them too. With no names it gives every one, in the unit's
order; with names, those passed under each, in the order named, and it closes the rest.

On macOS, `Activated` asks launchd for the sockets under each name, a key of the job's
`Sockets`, in the order named; each is named by its key. launchd hands a key's sockets once,
so a second call for it is refused, and it passes nothing unnamed, so a call with no names is
refused with `fs.ErrInvalid`.

On both, a name that is empty or named twice is refused with `fs.ErrInvalid`, and a descriptor
that is not a stream socket listening is refused. A process systemd or launchd did not start by
its socket, one passed nothing under a name asked for, and every process off Linux and macOS,
gets `ErrNotActivated`, which wraps `errors.ErrUnsupported`. On every refusal each descriptor
passed is closed.

## Libraries looked at

The standard library and `golang.org/x` first. The dates are each version's release.

| looked at | why it falls short, or what it gives | verdict |
|---|---|---|
| the standard library | `net/http` serves; nothing in it stops a server after a span with nothing in flight, or reads `LISTEN_FDS`; `net.Listen` binds a socket with the umask's mode until a chmod | the idle exit is written here on it (about 60 lines) |
| `github.com/coreos/go-systemd/v22` v22.7.0 (27 Jan 2026), `activation` | takes the variables out of the environment too, and names each socket | **used** |
| go-systemd's `daemon` | says readiness and watchdog, not idleness | not taken |
| `github.com/bored-engineer/go-launchd` v0.0.0-20241220214420-b514778f4f80 (20 Dec 2024), `Files` | launchd's `launch_activate_socket`, called without cgo through an assembly trampoline the tree's file check refuses in listen itself, so a port is not possible and the module is imported; its `Sockets` leaks the files and listeners made when one fails, so listen takes `Files` alone and makes each listener itself | **used**, `Files` alone, on macOS |
| `github.com/docker/go-connections` v0.8.2 (8 Oct 2026), `sockets` | binds a Unix socket with no access, gives it its mode and group, and only then listens; but `NewUnixSocketWithOpts` first unlinks whatever is at the path, a live socket bound there since `Listen` looked included, so listen binds and listens itself, in a private folder, and puts the socket at the path by a hard link, and takes only its option type, `sockets.SockOption`, and `WithChmod`, giving the group by an option of its own; on Windows it sets a socket's access list by path, after the bind, so listen takes no Unix socket there | **used**, its options alone, on Unix |
| `github.com/Microsoft/go-winio` v0.6.4 | a named pipe made with its security descriptor and `FILE_CREATE`, refusing remote clients; peer already imports it | **used**, on Windows |
| `github.com/gofrs/flock` v0.13.2 | a lock file held by the open file, let go when the process ends, on Unix and Windows | **used** |
| `alexflint/go-filemutex`, `juju/fslock` | a lock file each, but each opens it following a link and skips the checks listen makes of the lock, and `juju/fslock` reaches `LockFileEx` by its DLL's name and a raw call | not taken, and banned for security |
| `nightlyone/lockfile` | a pid file, not a kernel lock, so a lock left by a dead or reused pid is held or taken wrongly | not taken, and banned for correctness |
| `cloudflare/tableflip` | a binary's upgrade in place, a different job, and it takes inherited descriptors as its parent's pipes and listeners with no check that they are this process's, leaving its variable to every subprocess | not taken, and banned for security |
| `ory/graceful`, `pseidemann/finish` | a serve and stop that `net/http` already gives | not taken, for a preference, so not banned |
| `libp2p/go-socket-activation` | the passed sockets through go-systemd's own `activation.Files`, with nothing more | not taken, for a preference, so not banned |
| `systemd-socket-proxyd` | no module: a proxy in front of the socket, so the peer a server reads is the proxy, not the caller, and every peer check passes or fails on the proxy | not taken; a program that puts it in front of listen loses `peer`'s answer |

The verdict: go-systemd's activation, go-launchd's `Files`, go-connections' socket options and flock's lock, wrapped;
the five rules, the bind that removes nothing and the idle exit written here.
