package caller

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"mime"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// capability is a capability a program asks serve to pass on.
const capability = "example.com/cap/app"

// servedBy is the headers serve writes for login and caps, each as serve
// encodes it; an empty one is left out, as serve leaves it.
func servedBy(login, caps string) http.Header {
	h := http.Header{}
	if login != "" {
		h.Add(HeaderLogin, mime.QEncoding.Encode("utf-8", login))
	}
	if caps != "" {
		h.Add(HeaderCaps, mime.QEncoding.Encode("utf-8", caps))
	}
	return h
}

// valuesOf is s's values as text, to compare.
func valuesOf(s Served) []string {
	var out []string
	for _, v := range s.Values {
		out = append(out, string(v))
	}
	return out
}

// TailscaleServe reads the login and the asked capability's values serve
// wrote, a plain ASCII value and an encoded one alike; an absent capabilities
// header leaves Had false and Values nil, an absent capability Values nil,
// a capability with no values or with null an empty list, a tagged node's
// request an empty Login, and a Funnel request an empty Login with Funnel set.
//
// What it costs when this fails: a program refuses a person serve vouched
// for, takes one person's grants for another's, or reads "no grant" and "no
// header" as the same answer.
func TestTailscaleServeReadsTheCallerServeWrote(t *testing.T) {
	t.Parallel()
	long := `{"` + capability + `":[{"role":"zoë","note":"` + strings.Repeat("ünï ", 40) + `"}]}`
	cases := []struct {
		name   string
		h      http.Header
		login  string
		values []string
		had    bool
	}{
		{"a plain login and a grant", servedBy("person@example.com", `{"`+capability+`":[{"role":"admin"},"x"]}`), "person@example.com", []string{`{"role":"admin"}`, `"x"`}, true},
		{"an encoded login", servedBy("zoë@example.com", ""), "zoë@example.com", nil, false},
		{"a long encoded grant, written as several words", servedBy("zoë@example.com", long), "zoë@example.com", []string{`{"role":"zoë","note":"` + strings.Repeat("ünï ", 40) + `"}`}, true},
		{"a tagged node, with no login", servedBy("", `{"`+capability+`":[1]}`), "", []string{"1"}, true},
		{"no capabilities header", servedBy("person@example.com", ""), "person@example.com", nil, false},
		{"another capability alone", servedBy("person@example.com", `{"example.com/cap/other":[1]}`), "person@example.com", nil, true},
		{"a request through Funnel", http.Header{HeaderFunnel: {"?1"}}, "", nil, false},
	}
	for _, c := range cases {
		s, err := TailscaleServe(c.h, capability, 0)
		funnel := c.h.Get(HeaderFunnel) != ""
		if err != nil || s.Login != c.login || s.Had != c.had || s.Funnel != funnel || !slices.Equal(valuesOf(s), c.values) || (c.values == nil) != (s.Values == nil) {
			t.Errorf("%s: %+v %v, want login %q, values %q, had %v", c.name, s, err, c.login, c.values, c.had)
		}
	}
	for _, values := range []string{`[]`, `null`} {
		s, err := TailscaleServe(servedBy("person@example.com", `{"`+capability+`":`+values+`}`), capability, 0)
		if err != nil || s.Values == nil || len(s.Values) != 0 || !s.Had {
			t.Errorf("a capability whose values are %s: %+v %v, want an empty list, not nil", values, s, err)
		}
	}
}

// A login or capabilities header written twice is an error, not the first
// or the last of the two.
//
// What it costs when this fails: a caller's own copy, sent beside the one
// serve writes and passed on by a proxy that adds rather than replaces, is
// read as who they are.
func TestTailscaleServeRefusesAHeaderWrittenTwice(t *testing.T) {
	t.Parallel()
	refused := 0
	for _, name := range []string{HeaderLogin, HeaderCaps} {
		h := servedBy("person@example.com", `{"`+capability+`":[1]}`)
		h.Add(name, h.Get(name))
		if s, err := TailscaleServe(h, capability, 0); err == nil {
			t.Errorf("%s twice: read as %+v", name, s)
		}
		refused++
	}
	if refused != 2 {
		t.Fatalf("held %d of 2 headers", refused)
	}
	if _, err := TailscaleServe(servedBy("person@example.com", `{"`+capability+`":[1]}`), capability, 0); err != nil {
		t.Errorf("each header once: %v", err)
	}
}

// A capabilities document with a key written twice, at the top or deeper, or
// once plain and once escaped, is an error, and no code of ours looks for one:
// encoding/json/v2 refuses it, and the test pins that, so a v2 that stopped is
// seen. So is a document that is not one JSON object, null among them, and one
// in which any capability's values, the asked one's or another's, are not a
// list or null.
//
// What it costs when this fails: a grant reads one way to the program and
// another to whatever else reads it, and the second key decides; or a null
// document, or a grant of the wrong shape, reads as no grant at all.
func TestTailscaleServeRefusesACapabilitiesDocumentThatReadsTwoWays(t *testing.T) {
	t.Parallel()
	twice := `{"` + capability + `":[{"role":"viewer"}],"` + capability + `":[{"role":"admin"}]}`
	var caps map[string][]jsontext.Value
	if err := json.Unmarshal([]byte(twice), &caps); !errors.Is(err, jsontext.ErrDuplicateName) {
		t.Fatalf("encoding/json/v2 no longer refuses a name written twice (%v): the reader needs a check of its own again", err)
	}
	docs := map[string]string{
		"a capability twice":          twice,
		"a capability twice, escaped": `{"` + capability + `":[1],"example.com/cap/app":[2]}`,
		"a key twice in a grant":      `{"` + capability + `":[{"role":"viewer","role":"admin"}]}`,
		"a key twice in another":      `{"example.com/cap/other":[{"a":1,"a":2}],"` + capability + `":[1]}`,
		"two documents":               `{"` + capability + `":[1]}{}`,
		"a list, not an object":       `[1]`,
		"null, not an object":         `null`,
		"the values not a list":       `{"` + capability + `":{"role":"admin"}}`,
		"another's not a list":        `{"example.com/cap/other":{"role":"admin"},"` + capability + `":[1]}`,
		"not JSON":                    `{"` + capability + `":[1]`,
	}
	for name, doc := range docs {
		if s, err := TailscaleServe(servedBy("person@example.com", doc), capability, 0); err == nil {
			t.Errorf("%s: read as %+v", name, s)
		}
	}
	if len(docs) != 10 {
		t.Fatalf("held %d documents", len(docs))
	}
	if _, err := TailscaleServe(servedBy("person@example.com", `{"`+capability+`":[{"role":"viewer"}],"example.com/cap/other":[{"role":"viewer"}]}`), capability, 0); err != nil {
		t.Errorf("the same key in two objects, once in each: %v", err)
	}
}

// Every value is decoded as RFC 2047, and one that is not what serve writes
// for what it decodes to is an error: the known collision, a literal encoded
// word spelling ASCII, which serve passes as it is since it is ASCII, an
// encoding serve does not write, a word whose bytes are not UTF-8, which serve
// never writes and which re-encodes to itself, and a header left empty. ASCII that is no
// whole encoded word is read as it is. A literal word spelling serve's
// encoding of text that is not ASCII is that text's very header, and is read
// as the text, as the README says; the negative half pins it.
//
// What it costs when this fails: a person whose login spells an encoded word
// is read as the login it spells, or a non-ASCII login is read as its encoded
// form and matches nobody.
func TestTailscaleServeReadsOneEncodingAsServeWritesIt(t *testing.T) {
	t.Parallel()
	refused := 0
	for name, raw := range map[string]string{
		"the known collision, a literal word spelling ASCII": "=?utf-8?q?person@example.com?=",
		"a word in base64":          "=?utf-8?b?em/Dq0BleGFtcGxlLmNvbQ==?=",
		"a word in another charset": "=?iso-8859-1?q?zo=EB@example.com?=",
		"a word that is not UTF-8":  "=?utf-8?q?zo=EB@example.com?=",
		"an empty login":            "",
	} {
		h := http.Header{HeaderLogin: {raw}}
		if s, err := TailscaleServe(h, capability, 0); err == nil {
			t.Errorf("%s (%q): read as %+v", name, raw, s)
		}
		refused++
	}
	if refused != 5 {
		t.Fatalf("held %d of 5 logins", refused)
	}
	h := http.Header{HeaderCaps: {`=?utf-8?q?{"` + capability + `":[1]}?=`}}
	if s, err := TailscaleServe(h, capability, 0); err == nil {
		t.Errorf("a capabilities document in a literal encoded word: read as %+v", s)
	}
	// The negative half: each form serve writes is read.
	s, err := TailscaleServe(http.Header{HeaderLogin: {"=?utf-8?q?zo=C3=AB@example.com?="}}, capability, 0)
	if err != nil || s.Login != "zoë@example.com" {
		t.Errorf("serve's encoding of a non-ASCII login: %+v %v", s, err)
	}
	s, err = TailscaleServe(http.Header{HeaderLogin: {"person@example.com"}}, capability, 0)
	if err != nil || s.Login != "person@example.com" {
		t.Errorf("a plain ASCII login: %+v %v", s, err)
	}
	s, err = TailscaleServe(http.Header{HeaderLogin: {"=?utf-8?q?zo@example.com"}}, capability, 0)
	if err != nil || s.Login != "=?utf-8?q?zo@example.com" {
		t.Errorf("a login that opens an encoded word and never closes it is read as it is spelt: %+v %v", s, err)
	}
}

// A login holding a zero-width space, a right-to-left override or a control,
// each as serve writes it, is refused with the words RemoteUser refuses the
// same name with, and a plain login and a non-ASCII one are still read.
//
// What it costs when this fails: a program that logs or shows the login, or
// an operator who copies one from a log line into a roles list, reads another
// person's name, while the program's other reader refuses it.
func TestTailscaleServeRefusesALoginThatIsNotPrintable(t *testing.T) {
	t.Parallel()
	const words = "is not UTF-8 or holds a control, format, separator, default ignorable or variation selector character"
	refused := 0
	for name, login := range map[string]string{
		"a zero-width space":        "admin\u200b@example.com",
		"a right-to-left override":  "admin\u202e@example.com",
		"a next-line control, U+85": "admin\u0085@example.com",
	} {
		h := servedBy(login, "")
		if h.Get(HeaderLogin) == login {
			t.Fatalf("%s: serve's encoding of %q is the login as it is, so the case reads nothing encoded", name, login)
		}
		if s, err := TailscaleServe(h, capability, 0); err == nil || !strings.Contains(err.Error(), words) {
			t.Errorf("%s: %+v %v, want an error that the login %s", name, s, err, words)
		}
		if _, err := authProxy().RemoteUser(http.Header{authUser: {login}}, true); err == nil || !strings.Contains(err.Error(), words) {
			t.Errorf("%s: RemoteUser gives %v, and the readers are to agree", name, err)
		}
		refused++
	}
	if refused != 3 {
		t.Fatalf("held %d of 3 logins", refused)
	}
	// The negative half: a printable login is read, ASCII or not.
	for _, login := range []string{"person@example.com", "zoë@example.com"} {
		if s, err := TailscaleServe(servedBy(login, ""), capability, 0); err != nil || s.Login != login {
			t.Errorf("%q: %+v %v, want it read", login, s, err)
		}
	}
}

// A name holding a character that prints as nothing, a default ignorable
// code point or a variation selector, none of them a control or format
// character, is refused by both readers: TailscaleServe and RemoteUser.
//
// What it costs when this fails: "admin" and "admin" with a Hangul filler or
// a variation selector print alike, so a log line, a page or a roles list an
// operator copies from names one person for another.
func TestTheScreenRefusesWhatPrintsAsNothing(t *testing.T) {
	t.Parallel()
	const words = "is not UTF-8 or holds a control, format, separator, default ignorable or variation selector character"
	refused := 0
	for name, login := range map[string]string{
		"a combining grapheme joiner, U+034F":    "admin\u034f@example.com",
		"a Hangul filler, U+3164":                "admin\u3164@example.com",
		"a Hangul choseong filler, U+115F":       "admin\u115f@example.com",
		"variation selector 16, U+FE0F":          "admin\ufe0f@example.com",
		"a Mongolian variation selector, U+180B": "admin\u180b@example.com",
		"variation selector 17, U+E0100":         "admin\U000e0100@example.com",
	} {
		if s, err := TailscaleServe(servedBy(login, ""), capability, 0); err == nil || !strings.Contains(err.Error(), words) {
			t.Errorf("%s: TailscaleServe reads %+v %v, want an error that the login %s", name, s, err, words)
		}
		if r, err := authProxy().RemoteUser(http.Header{authUser: {login}}, true); err == nil || !strings.Contains(err.Error(), words) {
			t.Errorf("%s: RemoteUser reads %+v %v, and the readers are to agree", name, r, err)
		}
		refused++
	}
	if refused != 6 {
		t.Fatalf("held %d of 6 logins", refused)
	}
}

// ServeHeaders names both headers the read believes, and a request carrying a
// caller's own copies of serve's headers, read as the caller wrote them, reads
// as no login and no capabilities header once the program drops ServeHeaders.
//
// What it costs when this fails: a caller who reaches the program other than
// through serve writes their own login or grants, and the program believes
// them.
func TestServeHeadersDropsEveryHeaderTheReadBelieves(t *testing.T) {
	t.Parallel()
	names := ServeHeaders()
	if !slices.Contains(names, HeaderLogin) || !slices.Contains(names, HeaderCaps) {
		t.Errorf("ServeHeaders %q leaves out a header the read believes", names)
	}
	h := servedBy("admin@example.com", `{"`+capability+`":[{"role":"admin"}]}`)
	h.Add("Tailscale-User-Name", "Admin")
	if s, err := TailscaleServe(h, capability, 0); err != nil || s.Login != "admin@example.com" || !s.Had {
		t.Fatalf("the caller's copies are not read before they are dropped (%+v %v): the case below shows nothing", s, err)
	}
	dropped := 0
	for _, name := range names {
		h.Del(name)
		dropped++
	}
	if dropped == 0 || len(h) != 0 {
		t.Fatalf("dropped %d headers, and %d are left", dropped, len(h))
	}
	if s, err := TailscaleServe(h, capability, 0); err != nil || s.Login != "" || s.Had || s.Values != nil {
		t.Errorf("a request with the caller's copies dropped: %+v %v, want no login and no header", s, err)
	}
}

// A capabilities header of MaxCaps bytes as it came is read, and one byte more
// is ErrTooLong, before it is decoded: the smallest header past the cap.
//
// What it costs when this fails: a caller's request makes the program decode
// and hold a grant document of any length serve passes on, or the program
// refuses a header serve wrote within the cap.
func TestTailscaleServeRefusesACapabilitiesHeaderPastMaxCaps(t *testing.T) {
	t.Parallel()
	doc := func(n int) string {
		head, tail := `{"`+capability+`":["`, `"]}`
		return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
	}
	s, err := TailscaleServe(servedBy("person@example.com", doc(MaxCaps)), capability, 0)
	if err != nil || len(s.Values) != 1 || len(s.Values[0]) != MaxCaps-len(`{"`+capability+`":[]}`) {
		t.Errorf("a header of MaxCaps bytes: %d values, %v, want it read", len(s.Values), err)
	}
	if s, err := TailscaleServe(servedBy("person@example.com", doc(MaxCaps+1)), capability, 0); !errors.Is(err, ErrTooLong) {
		t.Errorf("a header of MaxCaps+1 bytes: %d values, %v, want ErrTooLong", len(s.Values), err)
	}
}

// capsDoc is a grants document of n bytes, one value under capability.
func capsDoc(n int) string {
	head, tail := `{"`+capability+`":["`, `"]}`
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}

// A program's own cap holds the grants header to it, raised or lowered: a
// header of the cap is read and one byte more is ErrTooLong. A cap below 0
// or over net/http's DefaultMaxHeaderBytes is refused before any header is
// read, a header written twice among them.
//
// What it costs when this fails: a program whose grants run past 64 KiB is
// refused every caller, a lowered cap lets a longer document be decoded, or
// a cap no header could reach reads as if it held one.
func TestTailscaleServeHoldsTheGrantsToTheProgramsCap(t *testing.T) {
	t.Parallel()
	for _, most := range []int{100, MaxCaps + 1, 200 << 10, http.DefaultMaxHeaderBytes} {
		if s, err := TailscaleServe(servedBy("person@example.com", capsDoc(most)), capability, most); err != nil || len(s.Values) != 1 {
			t.Errorf("a header of %d bytes under a cap of %d: %d values, %v, want it read", most, most, len(s.Values), err)
		}
		if s, err := TailscaleServe(servedBy("person@example.com", capsDoc(most+1)), capability, most); !errors.Is(err, ErrTooLong) || s.Had || s.Values != nil {
			t.Errorf("a header of %d bytes under a cap of %d: %+v %v, want ErrTooLong", most+1, most, s.Had, err)
		}
	}
	twice := servedBy("person@example.com", capsDoc(100))
	twice.Add(HeaderLogin, "admin@example.com")
	refused := 0
	for _, limit := range []int{-1, -MaxCaps, http.DefaultMaxHeaderBytes + 1, 1 << 30} {
		for _, h := range []http.Header{servedBy("person@example.com", capsDoc(100)), twice, {}} {
			if s, err := TailscaleServe(h, capability, limit); err == nil || errors.Is(err, ErrTooLong) || !strings.Contains(err.Error(), "is no cap") || s.Login != "" || s.Had {
				t.Errorf("a cap of %d: %+v %v, want it refused as no cap", limit, s, err)
			}
		}
		refused++
	}
	if refused != 4 {
		t.Fatalf("held %d of 4 caps", refused)
	}
}

// TailscaleServe refuses a call that asks for no capability, which no grant is
// under.
//
// What it costs when this fails: a program that forgot to name its
// capability reads every caller as holding none, or the first it finds.
func TestTailscaleServeRefusesNoCapability(t *testing.T) {
	t.Parallel()
	if s, err := TailscaleServe(servedBy("person@example.com", `{"":[1]}`), "", 0); err == nil {
		t.Errorf("no capability: read as %+v", s)
	}
}

// A Funnel header is read as strictly as the others: once, exactly "?1",
// and never beside a login or grants, which serve does not write for a
// request through Funnel.
//
// What it costs when this fails: a request from anyone on the internet,
// which serve marks as Funnel's, reads as a caller of the tailnet with no
// login, or a caller's own copy of the header decides what it is.
func TestTailscaleServeReadsTheFunnelHeaderStrictly(t *testing.T) {
	t.Parallel()
	cases := map[string]http.Header{
		"another value":        {HeaderFunnel: {"?0"}},
		"an empty value":       {HeaderFunnel: {""}},
		"written twice":        {HeaderFunnel: {"?1", "?1"}},
		"beside a login":       {HeaderFunnel: {"?1"}, HeaderLogin: {"person@example.com"}},
		"beside grants":        {HeaderFunnel: {"?1"}, HeaderCaps: {`{"` + capability + `":[1]}`}},
		"a literal encoded ?1": {HeaderFunnel: {"=?utf-8?q?=3F1?="}},
	}
	for name, h := range cases {
		if s, err := TailscaleServe(h, capability, 0); err == nil {
			t.Errorf("%s: read as %+v", name, s)
		}
	}
	if len(cases) != 6 {
		t.Fatalf("held %d headers", len(cases))
	}
	s, err := TailscaleServe(http.Header{HeaderFunnel: {"?1"}}, capability, 0)
	if err != nil || !s.Funnel || s.Login != "" || s.Had {
		t.Errorf("serve's Funnel header alone: %+v %v", s, err)
	}
	if s, err := TailscaleServe(servedBy("person@example.com", ""), capability, 0); err != nil || s.Funnel {
		t.Errorf("a request with no Funnel header: %+v %v", s, err)
	}
}
