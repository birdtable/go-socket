package caller

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LoginProxy is how a login proxy in front of a program writes the caller:
// the header that names the user it logged in, the header that holds their
// groups, and the character between two groups in it. All three are the
// program's own, as its proxy writes them (Authelia's "Remote-User",
// "Remote-Groups" and ',', or authentik's "X-authentik-username",
// "X-authentik-groups" and '|'): there is no default. A proxy that writes no
// groups header is said to with NoGroups, and names neither a groups header
// nor a separator. Each name is an HTTP token, and the two are two headers,
// or RemoteUser reads nobody and is an error. The proxy must overwrite or
// drop each header it names whenever a visitor sends it.
type LoginProxy struct {
	// User is the header that names the user the proxy logged in.
	User string
	// Groups is the header that holds their groups, Separator between each
	// two.
	Groups string
	// Separator is the character between two groups in Groups: '|', or an
	// ASCII mark that is not in an HTTP token, as ',' or ';'. A letter, a
	// digit, a space and a token's other marks are refused: a group's name
	// holds them.
	Separator rune
	// NoGroups is that the proxy writes no groups header, so none is read
	// and a caller has no groups. Groups and Separator are then empty.
	NoGroups bool
}

// Remote is the caller a login proxy forwarded.
type Remote struct {
	// User is the user the proxy logged in, spaces and tabs around it off; "" is nobody.
	User string
	// Groups is their groups as HTTP reads a list: spaces and tabs off, empty ones dropped.
	Groups []string
}

const ows = " \t" // what HTTP takes off a value or a list's element (RFC 9110, 5.6.3), and nothing else

// RemoteUser is the user and groups the login proxy wrote into h, in p's
// headers. fromProxy is whether the request came on the proxy's own
// listener, as the program knows it: on any other, nobody is believed,
// whatever h says. A p that check refuses is an error, wherever the request
// came. Either header written twice is an error: a proxy that overwrites
// what a visitor sent writes each once, so two are a visitor's beside the
// proxy's, and which is whose cannot be told. So a proxy that writes each
// group as its own header line is refused: put the groups in one header, as
// nginx auth_request joins them. An empty or absent user is nobody, with no
// groups. Behind a proxy of NoGroups, no groups header is
// read, whatever a visitor sends. A user or group that is not UTF-8 or holds
// a control, format, line or paragraph separator character, a default
// ignorable code point or a variation selector is an error, as in
// TailscaleServe: it prints as another name, or not at all. So is one that
// starts or ends with a space other than the space and the tab: taken off, it
// would make two names one, which is the program's to say; kept, it prints as
// the other.
func (p LoginProxy) RemoteUser(h http.Header, fromProxy bool) (Remote, error) {
	names, err := p.RemoteHeaders()
	if err != nil {
		return Remote{}, err
	}
	if !fromProxy {
		return Remote{}, nil
	}
	for _, name := range names {
		if n := len(h.Values(name)); n > 1 {
			return Remote{}, fmt.Errorf("the login proxy sent %s %d times, so a visitor's own copy came through beside it: "+
				"the proxy must overwrite or drop each of its headers a visitor sends", name, n)
		}
	}
	user := names[0]
	who := strings.Trim(h.Get(user), ows)
	switch {
	case who == "":
		return Remote{}, nil
	case !printable(who):
		return Remote{}, fmt.Errorf("the %s the login proxy sent is not UTF-8 or holds a control, format, separator, default ignorable or variation selector character", user)
	case strings.TrimFunc(who, unicode.IsSpace) != who:
		return Remote{}, fmt.Errorf("the %s the login proxy sent starts or ends with a space that is not a space or a tab", user)
	}
	r := Remote{User: who}
	if p.NoGroups {
		return r, nil
	}
	groups := names[1]
	for g := range strings.SplitSeq(h.Get(groups), string(p.Separator)) {
		switch g = strings.Trim(g, ows); {
		case !printable(g):
			return Remote{}, fmt.Errorf("a group of the %s the login proxy sent is not UTF-8 or holds a control, format, separator, default ignorable or variation selector character", groups)
		case strings.TrimFunc(g, unicode.IsSpace) != g:
			return Remote{}, fmt.Errorf("a group of the %s the login proxy sent starts or ends with a space that is not a space or a tab", groups)
		case g != "":
			r.Groups = append(r.Groups, g)
		}
	}
	return r, nil
}

// RemoteHeaders is the headers RemoteUser reads, in their canonical
// spelling: the user's, then the groups' unless p is NoGroups. A p that
// RemoteUser refuses is the same error here, and no names. A program that
// takes requests other than through the proxy deletes these from each
// before anything reads it, and with them any other header of its proxy's
// it reads itself: caller knows only these.
func (p LoginProxy) RemoteHeaders() ([]string, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	if p.NoGroups {
		return []string{http.CanonicalHeaderKey(p.User)}, nil
	}
	return []string{http.CanonicalHeaderKey(p.User), http.CanonicalHeaderKey(p.Groups)}, nil
}

// check is why p cannot be read: a header left out, a name that is not an
// HTTP token, the two names one header, a separator a group's name holds,
// or a groups header or separator beside NoGroups.
func (p LoginProxy) check() error {
	switch {
	case p.NoGroups && (p.Groups != "" || p.Separator != 0):
		return fmt.Errorf("a login proxy that writes no groups header names none, and no separator: %q and %q", p.Groups, p.Separator)
	case p.User == "" || !p.NoGroups && p.Groups == "":
		return errors.New("a login proxy names its user's header and its groups' header, or says it writes no groups header: there is no default")
	case !token(p.User) || !p.NoGroups && !token(p.Groups):
		return fmt.Errorf("%q or %q is not a header's name", p.User, p.Groups)
	case !p.NoGroups && http.CanonicalHeaderKey(p.User) == http.CanonicalHeaderKey(p.Groups):
		return fmt.Errorf("%s and %s are one header", p.User, p.Groups)
	case !p.NoGroups && !separator(p.Separator):
		return fmt.Errorf("%q is no separator of groups: give '|', or an ASCII mark that is not in an HTTP token, as ',' or ';'; there is no default", p.Separator)
	}
	return nil
}

// separator is whether c may stand between two groups: '|', which a proxy
// uses, or a printable ASCII mark that is not in an HTTP token, so no
// letter, digit or space.
func separator(c rune) bool {
	return c == '|' || ' ' < c && c < 0x7f && !tchar(c)
}

// token is whether s is an HTTP token (RFC 9110), as a header's name is.
func token(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(c rune) bool { return !tchar(c) })
}

// tchar is whether c may be in a token: a letter, a digit, or one of
// RFC 9110's sixteen marks.
func tchar(c rune) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)
}

// printable is whether s is UTF-8 and holds no control (Cc), format (Cf),
// line (Zl) or paragraph (Zp) separator character, no other default
// ignorable code point (Other_Default_Ignorable_Code_Point: U+034F, the
// Hangul fillers U+115F, U+1160, U+3164 and U+FFA0 among them) and no
// variation selector (Variation_Selector): text a page or a log prints as it
// reads, with no mark that hides or reorders a letter, no character that
// prints as nothing, and no break that starts a line of its own. A name
// holding one prints as another name or as none. The screen is these classes
// and not every character that looks like another: a no-break space (U+00A0)
// and the blank braille pattern (U+2800) pass it.
func printable(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, func(c rune) bool {
		return unicode.In(c, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp,
			unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector)
	})
}
