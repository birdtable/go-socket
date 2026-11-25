# caller

`caller` says who the caller of an HTTP request is, as a front the program trusts wrote it in
the request's headers: who `tailscale serve` says the caller is (`TailscaleServe`,
`ServeHeaders`), or who a login proxy says it is, in headers the program names (`LoginProxy`). It believes a header only as far as the
program knows who wrote it; package `peer`, which says who is at the other end of a connection,
is how a program knows that. The two share no code: a program joins them itself, the uid of
tailscaled's account (`TailscaledUID`, from the socket the program names) becoming a principal
with `peer.Unix`. It holds too a Tailscale version read as its two numbers (`ParseVersion`).

What is a business's own stays the program's, with no default here: what a grant's values mean
(roles, permissions or anything else), how permissions are named, when two logins are one, the oldest Tailscale it runs
on, where tailscaled's socket is, and the `tailscale serve` line that puts it behind serve. The
gate itself stays outside.

A program imports `github.com/birdtable/go-socket/caller`.

- **What it imports**: the standard library alone, and no other package of ours.
  The root [layers.yaml](../layers.yaml) says so.

`go doc github.com/birdtable/go-socket/caller` has the rest; every exported name is documented.

## How it works

Built on `net/http`'s headers, `mime`'s `WordDecoder` and `encoding/json/v2`, and on the
practice of a front that writes who the caller is in a request's headers, as `tailscale serve`
and login proxies do.

It adds a strict read of those headers, believed only as far as the program knows who wrote them.

The program configures the grant's capability name and its cap, a login proxy's header names and
groups separator, whether a request came through its front, and the path of tailscaled's socket.
How two logins compare is the program's too.

## What the calls promise

- **`TailscaleServe(h, capability, limit)`** is the caller `tailscale serve` wrote into a request's
  headers: the login (`HeaderLogin`), whether the request came through Funnel (`HeaderFunnel`,
  exactly `?1`), and the values under `capability` in the grants serve passes on (`HeaderCaps`).
  An empty login is serve naming no person: a tagged node, a Funnel caller from anyone on the
  internet, or a caller it could not name. It is never to be read as a member of the tailnet.
  `Had` says whether the grants header came at all, and `Values` is nil when the capability was
  not in it, and empty, not nil, when it was there with no values or with `null`.
  These are errors, never a caller:
  - a header written twice, never its first or last copy;
  - an empty login;
  - a login that is not UTF-8 or holds a control, format, line or paragraph separator
    character, a default ignorable code point or a variation selector, as `LoginProxy`
    refuses a name: serve encodes such a login and passes it on, and it prints as
    another name or as none;
  - a Funnel header that is not `?1`, or that comes with a login or grants;
  - a grants header longer than the cap, as it came, before it is decoded (`ErrTooLong`).
    `limit` is the cap: `0` holds the header to `MaxCaps`, 64 KiB (65,536 bytes), and any
    other cap from 1 to net/http's `DefaultMaxHeaderBytes`, 1 MiB, holds it to that; a cap
    below 0 or over 1 MiB, which net/http refuses a request's headers past before a handler
    runs, is refused before a header is read. `MaxCaps` is a safe ceiling, not a limit of
    serve's: a program whose grants run longer raises it by giving its own cap, and takes on
    decoding that much per request;
  - a grants document that is not one JSON object (`null` among them), has a name written
    twice in any object at any depth, escaped or not, or holds any capability's values as
    anything but a list or `null`. `encoding/json/v2` reads it, and refuses each of these
    itself but `null`.

  It believes the headers only as far as the program knows serve wrote them: a program reads
  them on a connection whose peer it has checked is serve's (`peer.Listener`), or drops
  `ServeHeaders` first.
- **`ServeHeaders()`** is every header serve drops from a caller's request and writes itself,
  the three `TailscaleServe` reads among them. A program that takes requests other than through
  serve deletes each from every request before anything reads it. A serve older than 1.92
  neither drops `HeaderCaps` nor writes it, so behind one a caller's own grants header comes
  through as if serve wrote it: a program that reads grants holds tailscaled to a floor of its
  own, 1.92 or later (`ParseVersion`).
- **`LoginProxy{User, Groups, Separator, NoGroups}`** is how a login proxy writes the caller:
  the header that names the user it logged in, the header that holds their groups, and the
  character between two groups in it. All three are the proxy's, and so the program's
  (Authelia's `Remote-User`, `Remote-Groups` and `,`, or authentik's `X-authentik-username`,
  `X-authentik-groups` and `|`): there is no default. A proxy that writes no groups header
  (Google IAP's `X-Goog-Authenticated-User-Email` alone) is named with `NoGroups`, and names no groups
  header and no separator; a groups header is never left out without it. The names are HTTP
  tokens and two headers, whatever their case. The separator is `|` or an ASCII mark that is
  not in an HTTP token (`,`, `;`, `/`): a letter, a digit, a space, a control, a character
  past ASCII and a token's other marks (`-`, `.`, `_`, `~`, `!`) are refused, as a group's
  name holds them. A `LoginProxy` that breaks any of this is an error from both calls, and
  `RemoteUser` reads nobody, on the proxy's listener and off it.
  - **`RemoteUser(h, fromProxy)`** is the user and groups the proxy wrote.
    `fromProxy` says whether the request came on the proxy's own listener, as the program
    knows it (`peer`, or a socket only the proxy reaches): on any other nobody is believed,
    whatever the headers say. Either header written twice is an error: a proxy writes each
    once, so a second is a visitor's copy that came through beside it. So a proxy that writes
    each group as its own header line is refused: put the groups in one header, as nginx
    `auth_request` joins them. The spaces and tabs
    around the user are taken off, and an empty or absent one is nobody, with no groups;
    groups are read as HTTP reads a list: split on the separator, the spaces and tabs around
    each taken off, empty ones dropped, so a group's name may hold any other mark. Nothing
    else is changed: two logins that differ only in case are two, and which are one person
    is the program's to say. Behind a
    proxy of `NoGroups` no groups header is read, whatever a visitor sends, and a caller has
    no groups. A user or group that is not UTF-8, or holds a control, format, line or
    paragraph separator character (a zero-width space or a right-to-left mark among them), a
    default ignorable code point (`U+034F`, and the Hangul fillers `U+115F`, `U+1160`,
    `U+3164` and `U+FFA0`) or a variation selector (`U+FE00` to `U+FE0F`, `U+180B` to
    `U+180D`, `U+E0100` to `U+E01EF`), is an error: each prints as another name or as none.
    The screen is these classes, not every character that looks like another: a no-break
    space (`U+00A0`) and the blank braille pattern (`U+2800`) pass it inside a name. A user
    or group that starts or ends with a space other than the space and the tab (a no-break
    space, say) is an error too: taken off, it would make two names one, and kept, it prints
    as the other.
  - **`RemoteHeaders()`** is the headers `RemoteUser` reads, in their canonical spelling: the
    user's, and the groups' unless `NoGroups`; a `LoginProxy` that `RemoteUser` refuses is the
    same error here, and no names, so a program never strips by a list that leaves the
    proxy's user header in. A program that takes requests other than through the proxy
    deletes these from every request before anything reads it, and with them any other
    header of its proxy's it reads itself (an email, a name): caller knows only these.
- **`ParseVersion(version)`** is the major and minor number of a Tailscale version, in the
  short form or the long one (`1.94.0-t0123abcd-g4567ef`); `ok` is false unless each is
  decimal digits alone (`+1.94`, ` 1.94`, `1` and a minor in other digits are no version).
  The floor is the program's, compared with the two numbers; the fact that bears on it is that
  below 1.92 serve passes a caller's own grants header on as if it wrote it. The version to
  read is tailscaled's, whose serve proxies the requests: the `Daemon:` line of
  `tailscale version --daemon`, or `Version` in `tailscale status --json`. The `tailscale`
  command's own version is not it: a new command can stand beside an older tailscaled still
  running. A program that reads both holds the lower to its floor.
- **`TailscaledUID(path)`** is the account tailscaled runs as: the owner of the socket at
  `path`, which the program gives. No file at `path` is nobody with no error; a file that is
  not a socket, or one that cannot be read, is nobody and an error. On Windows, and on every
  system that is not a unix one, it is `ErrNoOwner`, which wraps `errors.ErrUnsupported`.

### The one RFC 2047 rule

Serve writes a value that is plain ASCII as it is, and any other as RFC 2047 encoded words
(`mime.QEncoding`, UTF-8). `TailscaleServe` decodes every value, always
(`mime.WordDecoder.DecodeHeader`), and refuses one that is not exactly what serve would write
for the text it decodes to. So a login that is non-ASCII is read as its text, and a login that
is ASCII spelling an encoded word of ASCII, such as `=?utf-8?q?person@example.com?=`, which serve
passes unencoded, is refused rather than read as the text it spells. An encoding serve does not
write (base64, another character set) is refused too. ASCII that is no whole encoded word
decodes to itself, and is read as it is spelt.

One header is two logins, and no rule tells them apart: serve passes an ASCII login as it is,
so a login that spells serve's whole encoding of a login that is not ASCII, such as
`=?utf-8?q?zo=C3=AB@example.com?=`, comes as the very header serve writes for
`zoë@example.com`, and is read as that. Every such login ends in `?=`, as every encoded word
does, which no email domain can. A program whose identity provider is a public one trusts it
never to issue such a login. A program on a self-hosted identity provider, which may issue any
login it is told to, stops it where the login is issued: it refuses to issue a login `issued`
that decodes to anything else,

    d, err := new(mime.WordDecoder).DecodeHeader(issued)
    refuse := err != nil || d != issued

which refuses the login that spells `zoë@example.com`'s encoding and issues
`zoë@example.com` and `person@example.com`. No check on the login as read tells the two
apart. A program whose logins are all ASCII can instead refuse every login read that serve
would have encoded, `mime.QEncoding.Encode("utf-8", login) != login`, which is every login
read that is not ASCII. `FuzzTailscaleServe` holds this as the only way a header serve wrote
is read as another login.

One more pair is made before the header arrives: a login with a space or a tab at either end,
such as ` person@example.com`, and the same login without it. Serve passes an ASCII login as
it is, and `net/http` drops the space at either end of a header's value as it reads the
request, so `TailscaleServe` reads `person@example.com` for both. The advice is the same: the
identity provider issues no login with a space or a tab at either end, and a self-hosted one
refuses to issue one, `strings.TrimSpace(issued) != issued`.

`FuzzTailscaleServe` is the read's fuzz target. Its seeds, in
`testdata/fuzz/FuzzTailscaleServe/`, are the shapes the tests read and refuse, and run as tests
in every `go test`; a fuzz run is on demand,
`go test -run '^$' -fuzz '^FuzzTailscaleServe$' -fuzztime 60s ./caller/`, and a pull request
that wants one asks for it.

## Libraries looked at

The standard library first, then the modules weighed, with the version and the date where one
was recorded.

| looked at | why it falls short, or what it gives | verdict |
|---|---|---|
| the standard library's `mime.WordDecoder` and `encoding/json/v2`, Go 1.27.4 (12 Nov 2026) | the decoder reads RFC 2047 as serve writes it; `encoding/json/v2` refuses a name written twice in any object at any depth, escaped or not, where `encoding/json` keeps the last copy | both **used** |
| `tailscale serve`'s own writer, `ipn/ipnlocal/serve.go` of tailscale v1.104.6, read at d566ac8ac902 (12 Nov 2026) | the source of the six headers serve drops and writes, and of its encoding, which is only Go's `mime.QEncoding`; nothing in it is a reader to take | read, not taken |
| `tailscale.com/client/local`'s `WhoIs`, v1.104.6 (12 Nov 2026) | asks tailscaled who a peer is on every request, through the whole tailscale module, where `caller` reads what serve has already written | not taken |
| tsnet | each program its own tailnet node, which asks who a peer is, where `caller` reads what serve has already written | not taken |

The verdict: written here, on the standard library.
