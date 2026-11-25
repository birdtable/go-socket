# peer

`peer` says who is at the other end of a unix socket, from the kernel (`PeerOf` on Linux, macOS and FreeBSD), and of a Windows named pipe only the accounts a program lists may open (`ListenPipe`, `DialPipe`, `PeerOf`), dials this session's SSH agent (`DialAgent`), and closes every connection whose peer is not among the principals a program trusts as it is accepted (`Principals`, `Unix`, `Listener`). Who `tailscale serve` says the caller of a request is, package [`caller`](../caller/README.md) reads. The gate itself stays outside.

A program imports `github.com/birdtable/go-socket/peer`.

- **What it imports**: `golang.org/x/sys/unix`; on Windows `github.com/Microsoft/go-winio` and `golang.org/x/sys/windows`; and no other module of ours.
  The root [layers.yaml](../layers.yaml) says so.

`go doc github.com/birdtable/go-socket/peer` has the rest; every exported name is documented.

## How it works

Built on `golang.org/x/sys/unix`'s `GetsockoptUcred` and `GetsockoptXucred`, which read the
kernel's record of a Unix socket's peer. On Windows it is built on go-winio's named pipe and
`golang.org/x/sys/windows`' token and security calls.

It adds what they leave out: the checks of the kernel's answer that x/sys leaves to its caller,
the client's account on a pipe, read by impersonation, access lists set by SID with the SIDs that
name every caller refused, and the refusal of a peer before a byte is read.

The program configures the principals it trusts, the SIDs it lists, the pipe's name, and what it
is told of each refusal.

## What the calls promise

- **`PeerOf(c)`** is the peer at the other end of a unix socket, as the kernel recorded it
  when that end connected: `SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS and on FreeBSD (built and vetted there,
  not yet run). Its
  `User` is a principal, `uid:1000` as `Unix(1000)` writes it, and `UID()` reads the uid back
  from that spelling alone. On Linux it has the process too (`PID`, with `PIDKnown`), the one
  that connected, which may since have exited and its number gone to another; it decides
  nothing. Any other connection, a TCP one on loopback among them, is `ErrCannotSay`, never a
  peer: being local is not an identity. So is a unix socket the kernel recorded no peer for, a
  datagram one or a stream one never connected. On Linux, in a user namespace whose map is not
  the whole identity (a rootless container), the overflow uid (`65534` by default) is
  `ErrCannotSay` too: the kernel gives every user the namespace does not map as that uid, which
  is also the namespace's own `nobody`, so a peer of that uid is never read there, the
  namespace's `nobody` included. On macOS and FreeBSD the kernel's answer must be of
  version 0 with at least one group, or it is an error: x/sys leaves the bytes the kernel did
  not write as zero, which would read as root. On Windows, a named pipe's connection is read as
  the next bullets say. Elsewhere, the other BSDs, every connection is `ErrCannotSay`, which
  wraps `errors.ErrUnsupported`, so `Listener` refuses every connection.
- **`ListenPipe(name, sids...)`**, on Windows, listens on the named pipe `name`, `\\.\pipe\`
  and then a name with no `\` or `/` in it that does not end in a dot or a space, which Win32
  drops (`\\.\pipe\svc.` would open `\\.\pipe\svc`), that SYSTEM, the pipe's owner and the accounts
  `sids` name alone may open. Its access list is set as the pipe is made, so it is
  never open to anyone else, and it inherits nothing. SYSTEM may do anything. The pipe's
  owner, the default owner of the process's token, may read and write and make the pipe's
  next server end, as go-winio does at each `Accept`: the listener's own account, or
  `BUILTIN\Administrators` for an elevated administrator, whose unelevated processes hold that
  group for deny alone. Each SID may read and write the pipe and read
  its owner, `FILE_GENERIC_READ | FILE_WRITE_DATA`, and may not make a server end: on a pipe
  the bit of `FILE_APPEND_DATA`, 0x4, is `FILE_CREATE_PIPE_INSTANCE`, and no listed SID's entry
  holds it, so no listed account can serve the name, take a client, and be named by that
  client's `PeerOf` as the pipe's owner. The kernel refuses every other account at the dial. A
  pipe of that name already there, made by anybody, is an error, never joined, and clients on
  other machines are refused. No SID is refused; so is a SID not written as Windows writes it
  back (`S-1-5-21-...-1001`; an SDDL alias such as `SY` or `WD`, a sign, a leading zero, or
  anything after the SID), and a SID that names every caller or none: the null SID, Everyone,
  ANONYMOUS LOGON, Authenticated Users, `BUILTIN\Users`, INTERACTIVE and LOCAL. These seven are
  the common mistakes, not every wide group: This Organization (`S-1-5-15`), Local account
  (`S-1-5-113`), Console Logon (`S-1-2-1`), Remote Interactive Logon (`S-1-5-14`), Batch
  (`S-1-5-3`), Service (`S-1-5-6`), NTLM Authentication (`S-1-5-64-10`) and a domain's Domain
  Users are taken, and how wide a group is stays the caller's to judge. A group may be listed,
  a narrow one made for the program's callers say; `Listener` with `Principals` is the check
  that names a caller, and keeps only the accounts `Principals` names.
- **`DialPipe(ctx, name)`**, on Windows, dials the pipe `name` on this machine, asking for a
  listed SID's rights and no more, to read and write the pipe and read its owner, at the
  identification level: the listener can read who the caller is and cannot act as it. A name
  that is not `\\.\pipe\` and then a name, another machine's pipe, a path that leaves
  `\\.\pipe\`, or a name that ends in a dot or a space, is refused before anything is opened.
  It dials with static context tracking, so Windows keeps the account that dialled for the
  life of the connection.
- **`DialAgent(ctx, env)`** is a connection to this session's SSH agent, for
  `golang.org/x/crypto/ssh/agent`'s `NewClient`. It dials what `SSH_AUTH_SOCK`, read through
  `env` (`os.Getenv`, or a test's own), names: on Unix the agent's socket, an absolute path,
  cleaned before it is checked; on Windows a pipe on this machine, by `DialPipe`, so its name is
  checked as every pipe's is and it dials at the identification level: the agent reads who
  dialled and cannot act as it. Where `SSH_AUTH_SOCK` is unset or empty, Windows dials the pipe
  of its OpenSSH agent service, `\\.\pipe\openssh-ssh-agent`, and Unix has no agent. No agent,
  unset or not there, wraps `fs.ErrNotExist`. A relative path, and on Linux an abstract socket
  (`@` and a name, which any account may bind first), is refused with `fs.ErrInvalid`; on
  Windows a value that is not a pipe on this machine is `DialPipe`'s refusal; a nil `env` is
  `fs.ErrInvalid`. Nothing is dialled when a value is refused.
  On Windows, after either dial, the pipe `SSH_AUTH_SOCK` names or OpenSSH's, `DialAgent` reads the
  owner of the pipe it opened and takes it only when that is this process's own user (its token's
  user), SYSTEM (`S-1-5-18`) or `BUILTIN\Administrators` (`S-1-5-32-544`). Pipe names are one flat
  namespace in which any account may make a pipe at a name nobody serves, as at OpenSSH's while its
  service is stopped, which is how Windows ships it; so a pipe any other account owns is closed and
  refused, wrapping `fs.ErrPermission`, and the error names the owner's SID. The owner read is the
  pipe's first maker's, so an agent service that started later and joined a name another account
  already held is refused too. Which of SYSTEM and `BUILTIN\Administrators` owns the OpenSSH agent
  service's pipe, and what the agents' pipes' access lists hold, is unmeasured: Windows ships the
  service disabled, enabling it takes an administrator, and the tests do not run as one;
  1Password's agent serves no pipe until its owner turns it on. The rule takes both owners. A pipe
  whose access list lets another account make a server end
  (`FILE_CREATE_PIPE_INSTANCE`) is not refused: that check waits on a measurement of OpenSSH's,
  1Password's and Bitwarden's lists, so that it refuses no real agent.
  A `ctx` with no deadline is given one of 5 seconds for the dial: a pipe's name held and never
  served answers busy, and would keep the dial waiting for ever. An agent not there by then wraps
  `context.DeadlineExceeded`; a `ctx` with a deadline of its own keeps it.
  On Windows, Git for Windows' own ssh cannot reach a Windows agent's named pipe, so a product
  sets git's `core.sshCommand` to `peer.AgentSSHCommand()` when it isn't empty.
- **`AgentSSHCommand()`** is the ssh command that reaches the agent `DialAgent` dials, in the
  form git takes for `core.sshCommand` and `GIT_SSH_COMMAND`. On Windows it is Win32-OpenSSH's
  `<windows>/System32/OpenSSH/ssh.exe`, written with forward slashes, `<windows>` being the
  Windows folder as the system gives it, not the environment. A client that is not installed,
  or is not a file, wraps `fs.ErrNotExist` and names the path; a path holding a character a
  POSIX shell reads specially is refused with `fs.ErrInvalid`, not quoted. Elsewhere it is
  `""`: git's own ssh reads `SSH_AUTH_SOCK` as `DialAgent` does.
- **`PeerOf(c)` on a pipe**: on the listener's side `User` is the client's SID, read before a
  byte of the connection is. `PeerOf` impersonates the client on a goroutine locked to a thread
  of its own, opens that thread's token, reads its user and reverts; if `RevertToSelf` fails, or
  the thread still has a token after it, the process panics on that goroutine, where no caller
  can recover it, so a thread that stays someone else never runs on. A client that dialled
  anonymously, as go-winio's own dials do, is `ErrCannotSay` with an error that says to dial with
  `DialPipe`, and `Listener` refuses it. So is a client whose token holds its user for deny alone
  (`SE_GROUP_USE_FOR_DENY_ONLY`), as a restricted token may: it cannot act as that account, so it
  is never named as it. `PeerOf` names an account, not an integrity level: a low-integrity process
  of an account is that account. A client that dials with dynamic context tracking, its own
  `CreateFile` with `SECURITY_CONTEXT_TRACKING`, is named once the listener has read from the
  connection by the account its thread was when it last wrote, and Windows gives the listener no
  documented call that says which tracking a client chose. So `PeerOf` names the account that
  connected only before the first read: `Listener` calls it there, before a byte is read, and that
  order is the guarantee. A program that calls `PeerOf` after a read does not have it.
  Windows gives no process here that this package trusts:
  `PIDKnown` is false. `PeerOf` does not tell a restricted token from its account: a client whose
  token keeps its user enabled, restricted by its restricting SIDs alone, is named as its user.
  On the dialling side `User` is the account that owns the pipe, the one whose listener made it,
  whatever token of that account made it: one of low integrity, a restricted one where Windows
  lets it make the pipe (one restricted to a group it holds was refused every pipe), or one
  holding the user for deny alone, which Windows lets make a pipe and makes its owner. The
  deny-only refusal is the listener's side's alone. Only the pipe's owner may make a server end
  of the pipe, so a process holding that SID is the only other that could serve the name, and
  `PeerOf` names the
  same SID for it. For a listener in an elevated administrator's process, the
  pipe's owner is the elevated token's default owner, which is `BUILTIN\Administrators` unless the token sets
  another, as Microsoft documents it for the token's owner
  ([TOKEN_OWNER](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-token_owner)).
  That rule was not measured here; for a listener started without elevation, the owner is the
  account that started it, as the Windows tests measure it. Off Windows, `ListenPipe` and `DialPipe` are
  `ErrCannotSay`.
- **`Listener(l, p, refused)`** closes each connection whose peer is not one of `p`'s principals
  as it is accepted, before a byte of it is read, on the goroutine that called `Accept`, and
  tells `refused` the peer's principal and whether `PeerOf` read one (`""` and `false` when it
  could not). A peer `PeerOf` cannot read is refused. Its `Accept` returns only a trusted
  connection, `l`'s own error, or the error of closing a refused one.
- **`Principals(ps...)`** trusts the peers whose principal is one of `ps`, spelt exactly, a
  Unix principal or a Windows SID, and nothing else. No principal, and the zero `Peers`, trust nobody.

## Libraries looked at

The standard library and `golang.org/x` first, then the modules weighed, with the version and
the date where one was recorded.

| looked at | why it falls short, or what it gives | verdict |
|---|---|---|
| the standard library | `syscall.GetsockoptUcred` reads `SO_PEERCRED`, but `syscall` is frozen, and Go keeps such calls current in `golang.org/x/sys/unix` | `golang.org/x/sys/unix.GetsockoptUcred` **used** |
| `golang.org/x/sys/unix.GetsockoptXucred`, v0.47.0 (12 Nov 2026) | reads `LOCAL_PEERCRED` on macOS and FreeBSD but drops the length of the kernel's answer, so an answer the kernel cut short reads as version 0 and uid 0, and a TCP socket's answer on macOS reads as version 0 with no group; the user comes before the count of groups and every process has one, so an answer with at least one group has its user written | **used**, behind a check that the socket is a unix one and that the answer is of version 0 with at least one group |
| `github.com/tailscale/peercred` (9 Nov 2026) | no version to pin, and its last change 22 months old | not taken |
| `golang.org/x/sys/windows`, v0.49.0 (6 Oct 2026) | wraps `OpenThreadToken`, `GetTokenUser`, `RevertToSelf`, `GetSecurityInfo` and `StringToSid`, but not `ImpersonateNamedPipeClient`, which is one lazy proc from advapi32 in System32 | **used** |
| `github.com/Microsoft/go-winio`, v0.6.4 (13 Oct 2026) | makes the pipe with its access list as the first instance, refuses remote clients, dials at a chosen impersonation level, and hands out the handle through `Fd()` | **used**, for the pipe and the dial |
| `github.com/natefinch/npipe` | no dial at a chosen impersonation level, and no access list set as the pipe is made | not taken |

The verdict: written here, on x/sys/unix, and on Windows on go-winio and x/sys/windows.
