package caller

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"unicode/utf8"
)

// The headers tailscale serve writes the caller in, on each request it
// proxies, after it drops any copy the caller sent.
const (
	// HeaderLogin is the caller's login, for a node a person owns. Serve
	// writes none when it names no person: for a tagged node, a Funnel
	// caller, or one it could not name. Its absence never means a member of
	// the tailnet.
	HeaderLogin = "Tailscale-User-Login"
	// HeaderCaps is the caller's grants under the capabilities serve was told
	// to pass on (--accept-app-caps), a JSON object of each one's values.
	HeaderCaps = "Tailscale-App-Capabilities"
	// HeaderFunnel is "?1" on a request that came through Funnel, from
	// anyone on the internet; serve writes no login and no grants with it.
	HeaderFunnel = "Tailscale-Funnel-Request"
)

// MaxCaps is the cap TailscaleServe holds a capabilities header to when the
// program gives none: 64 KiB, 65,536 bytes as it came, before it is decoded.
// It is a safe ceiling, not a limit of serve's: a program whose grants run
// longer raises it by giving its own cap, up to net/http's
// DefaultMaxHeaderBytes, 1 MiB, and takes on decoding that much per request.
const MaxCaps = 64 << 10

// capsCeiling is the highest cap TailscaleServe takes: net/http's
// DefaultMaxHeaderBytes, past which net/http refuses a request's headers
// before a handler runs, so no grants header that long comes to one.
const capsCeiling = http.DefaultMaxHeaderBytes

// ErrTooLong is a capabilities header longer than the cap TailscaleServe
// was given, MaxCaps when it was given none.
var ErrTooLong = fmt.Errorf("the %s header is longer than the cap", HeaderCaps)

// Served is the caller of a request tailscale serve proxied, as serve wrote
// it in the request's headers.
type Served struct {
	// Login is the caller's login. "" is serve naming no person: a tagged
	// node, a Funnel caller, or one it could not name. It is never read as a
	// member of the tailnet.
	Login string
	// Values is the values under the capability asked for, in the order serve
	// wrote them; nil when it was not there, and empty, not nil, when it was
	// with none or with null.
	Values []jsontext.Value
	// Had is whether the capabilities header came at all: serve writes it
	// only when it was told to pass capabilities on. A serve older than 1.92
	// neither writes it nor drops a caller's own, so behind one Had and
	// Values may be the caller's (ParseVersion).
	Had bool
	// Funnel is whether serve said the request came through Funnel, from
	// anyone on the internet.
	Funnel bool
}

// TailscaleServe is the caller tailscale serve wrote into h, and the values
// of capability among its grants. It believes h only as far as the program
// knows serve wrote it: a program reads it from a connection whose peer it
// has checked is serve's (package peer's Listener), or drops ServeHeaders from
// every request that came another way first.
//
// Every value is read as serve writes it, an RFC 2047 encoded word for one
// that is not plain ASCII and the value as it is otherwise: it is decoded
// always, and a value that is not the one serve would write for what it
// decodes to is an error, so a literal encoded word that spells ASCII is
// never read as the text it spells. One that spells serve's whole encoding of
// text that is not ASCII is the very header serve writes for that text, and
// is read as it: serve writes the two logins alike, and no reader of the
// header can tell them apart (the README says what a program does about it).
//
// A header written twice is an error, as is an empty login, a login that is
// not printable text, a Funnel header that is not "?1" or comes with a login
// or grants, and a capabilities header longer than the cap (ErrTooLong). The
// cap is limit, in bytes as the header came, before it is decoded: 0 holds it
// to MaxCaps, and 1 to net/http's DefaultMaxHeaderBytes (1 MiB) hold it to
// that; a limit below 0 or over it is refused before a header is read. A
// login is screened as LoginProxy.RemoteUser screens a name:
// one holding a control, format, line or paragraph separator character, a
// default ignorable code point (a Hangul filler among them) or a variation
// selector, which serve encodes and passes on, prints as another name or as
// none, and is refused.
// The capabilities document is read by encoding/json/v2: one JSON object, never
// null, with no name written twice in any object at any depth, whose every
// capability's values are a list or null. Null values are read as an empty
// list, as serve writes a grant with none.
func TailscaleServe(h http.Header, capability string, limit int) (Served, error) {
	most, err := capsCap(limit)
	if err != nil {
		return Served{}, err
	}
	if capability == "" {
		return Served{}, errors.New("no capability was asked for: name the one whose values are read")
	}
	var s Served
	login, had, err := served(h, HeaderLogin, most)
	switch {
	case err != nil:
		return Served{}, err
	case had && login == "":
		return Served{}, fmt.Errorf("the %s header is empty, as serve writes it for a login that is not UTF-8", HeaderLogin)
	case had && !printable(login):
		return Served{}, fmt.Errorf("the %s header is not UTF-8 or holds a control, format, separator, default ignorable or variation selector character, and a name is printable text", HeaderLogin)
	}
	s.Login = login
	funnel, hadFunnel, err := served(h, HeaderFunnel, most)
	switch {
	case err != nil:
		return Served{}, err
	case hadFunnel && funnel != "?1":
		return Served{}, fmt.Errorf("the %s header is %q, and serve writes it only as \"?1\"", HeaderFunnel, funnel)
	}
	s.Funnel = hadFunnel
	caps, had, err := served(h, HeaderCaps, most)
	switch {
	case err != nil:
		return Served{}, err
	case s.Funnel && (s.Login != "" || had):
		return Served{}, errors.New("a request through Funnel came with a login or grants, which serve never writes for one")
	case !had:
		return s, nil
	}
	s.Had = true
	if s.Values, err = capValues([]byte(caps), capability); err != nil {
		return Served{}, err
	}
	return s, nil
}

// capsCap is the cap limit gives: MaxCaps for 0, limit for 1 to
// capsCeiling, and an error for any other.
func capsCap(limit int) (int, error) {
	switch {
	case limit == 0:
		return MaxCaps, nil
	case limit < 0 || limit > capsCeiling:
		return 0, fmt.Errorf("a cap of %d bytes on the %s header is no cap: give 1 to %d, or 0 for %d", limit, HeaderCaps, capsCeiling, MaxCaps)
	}
	return limit, nil
}

// ServeHeaders is every header tailscale serve drops from a caller's request
// and writes itself, the three TailscaleServe reads among them. A program that
// takes requests other than through serve deletes these from each before
// anything reads it, so a caller's own copies are never believed. A serve
// older than 1.92 does not drop HeaderCaps, so behind one a caller's own
// grants header comes through: a program that reads grants holds tailscaled
// to a floor of its own, 1.92 or later (ParseVersion).
func ServeHeaders() []string {
	return []string{
		HeaderLogin,
		"Tailscale-User-Name",
		"Tailscale-User-Profile-Pic",
		HeaderFunnel,
		"Tailscale-Headers-Info",
		HeaderCaps,
	}
}

// served is the value of the header name in h, decoded as serve encodes it,
// and whether the header came; a header written twice is an error, and so is
// a capabilities header longer than most, before it is decoded.
func served(h http.Header, name string, most int) (string, bool, error) {
	vs := h.Values(name)
	switch len(vs) {
	case 0:
		return "", false, nil
	case 1:
	default:
		return "", false, fmt.Errorf("the %s header came %d times, and serve writes it once", name, len(vs))
	}
	if name == HeaderCaps && len(vs[0]) > most {
		return "", false, fmt.Errorf("%w: %d bytes as it came, over the %d read", ErrTooLong, len(vs[0]), most)
	}
	var d mime.WordDecoder
	v, err := d.DecodeHeader(vs[0])
	if err != nil {
		return "", false, fmt.Errorf("the %s header is not RFC 2047 as serve writes it: %w", name, err)
	}
	if !utf8.ValidString(v) || mime.QEncoding.Encode("utf-8", v) != vs[0] {
		return "", false, fmt.Errorf("the %s header is not the value serve writes for what it decodes to", name)
	}
	return v, true, nil
}

// capValues is the list under capability in doc, a JSON object of each
// capability's list; nil when capability is not a key of it, and empty when
// its values are null. encoding/json/v2 refuses a name written twice in any
// object of doc, at any depth, and any capability whose values are not a list.
func capValues(doc []byte, capability string) ([]jsontext.Value, error) {
	var caps map[string][]jsontext.Value
	if err := json.Unmarshal(doc, &caps); err != nil {
		return nil, fmt.Errorf("the %s header is not one JSON object of lists: %w", HeaderCaps, err)
	}
	if caps == nil {
		return nil, fmt.Errorf("the %s header is null, not an object", HeaderCaps)
	}
	values, ok := caps[capability]
	switch {
	case !ok:
		return nil, nil
	case values == nil:
		return []jsontext.Value{}, nil
	}
	return values, nil
}
