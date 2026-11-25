package caller

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"mime"
	"net/http"
	"slices"
	"testing"
	"unicode/utf8"
)

// Whatever headers a request carries, TailscaleServe never panics, and what
// it reads is what serve would have written: a login is the text whose
// serve encoding is the header as it came, a capabilities header it reads is
// no longer than MaxCaps and holds one JSON object, not null, with every key
// once at every depth and every capability's values a list or null, and gives
// the values encoding/json gives, null as an empty list; a Funnel request is
// exactly "?1" with no login and no grants, and a header written twice is
// refused. A login serve itself encodes is read as that login or refused,
// never read as another. login, caps and funnel are each header's value as it
// came, "" for one that did not; twice names the header written a second time
// (1 the login, 2 the capabilities, 3 Funnel). The oracle reads the document
// with encoding/json, not the v2 the reader uses, and counts the keys written
// itself. The seeds in testdata/fuzz/FuzzTailscaleServe run as tests in every
// go test; a fuzz run is on demand (go test -run '^$' -fuzz
// '^FuzzTailscaleServe$').
//
// What it costs when this fails: a caller who writes their own headers, or a
// login that spells an encoded word, is read as another person, or a grant
// reads one way to the program and another to whatever else reads it.
func FuzzTailscaleServe(f *testing.F) {
	f.Fuzz(func(t *testing.T, login, caps, funnel string, twice uint8) {
		h := http.Header{}
		for name, v := range map[string]string{HeaderLogin: login, HeaderCaps: caps, HeaderFunnel: funnel} {
			if v != "" {
				h[name] = []string{v}
			}
		}
		doubled := map[uint8]string{1: HeaderLogin, 2: HeaderCaps, 3: HeaderFunnel}[twice%4]
		if vs := h[doubled]; len(vs) == 1 {
			h[doubled] = append(vs, vs[0])
		}
		s, err := TailscaleServe(h, capability, 0)
		if err != nil {
			if s.Login != "" || s.Values != nil || s.Had || s.Funnel {
				t.Errorf("refused with %v, and read %+v", err, s)
			}
			return
		}
		if len(h[doubled]) == 2 {
			t.Errorf("%s written twice: read as %+v", doubled, s)
		}
		if len(caps) > MaxCaps {
			t.Errorf("a capabilities header of %d bytes, past MaxCaps, read as %+v", len(caps), s)
		}
		if s.Login != "" && mime.QEncoding.Encode("utf-8", s.Login) != login || s.Login == "" && login != "" {
			t.Errorf("the login header %q read as %q, which is not what serve writes for it", login, s.Login)
		}
		if s.Funnel != (funnel != "") || s.Funnel && (funnel != "?1" || s.Login != "" || s.Had) {
			t.Errorf("the Funnel header %q read as %+v", funnel, s)
		}
		if s.Had != (caps != "") || !s.Had && s.Values != nil {
			t.Errorf("the capabilities header %q read as %+v", caps, s)
		}
		if s.Had {
			readAsJSONReads(t, caps, s.Values)
		}
		servesOwnLogin(t, login)
	})
}

// readAsJSONReads fails t unless caps, a capabilities header TailscaleServe
// read as values, decodes to one JSON object, not null, with each key written
// once at every depth and every capability's values a list or null, whose
// list under capability is values, as encoding/json reads it, with null read
// as an empty list.
func readAsJSONReads(t *testing.T, caps string, values []jsontext.Value) {
	t.Helper()
	var d mime.WordDecoder
	doc, err := d.DecodeHeader(caps)
	if err != nil {
		t.Fatalf("the capabilities header %q was read, and does not decode: %v", caps, err)
	}
	var tree any
	if err := json.Unmarshal([]byte(doc), &tree); err != nil {
		t.Fatalf("the capabilities document %q was read, and is not JSON: %v", doc, err)
	}
	written, err := keysWritten(json.NewDecoder(bytes.NewReader([]byte(doc))))
	if err != nil || written != entries(tree) {
		t.Errorf("the capabilities document %q was read, and has %d keys written for %d it decodes to (%v): a key is written twice", doc, written, entries(tree), err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &object); err != nil || object == nil {
		t.Fatalf("the capabilities document %q was read, and is not an object: %v", doc, err)
	}
	var want []json.RawMessage
	for name, raw := range object {
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatalf("the capabilities document %q was read, and its values of %q are neither a list nor null: %v", doc, name, err)
		}
		if name == capability {
			want = append([]json.RawMessage{}, list...)
		}
	}
	got := make([]json.RawMessage, 0, len(values))
	for _, v := range values {
		got = append(got, json.RawMessage(v))
	}
	if (want == nil) != (values == nil) || !slices.EqualFunc(got, want, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
		t.Errorf("the capabilities document %q read as %q, and encoding/json reads %q", doc, values, want)
	}
}

// servesOwnLogin fails t if serve's own encoding of login, a header serve
// would write for a person whose login is that text, is refused although serve
// had to encode it, or is read as another login in any way but the one the
// README says no reader can avoid: a login serve passes as it is that spells
// serve's encoding of the login it is read as, one serve encodes.
func servesOwnLogin(t *testing.T, login string) {
	t.Helper()
	if login == "" || !utf8.ValidString(login) {
		return
	}
	written := mime.QEncoding.Encode("utf-8", login)
	s, err := TailscaleServe(http.Header{HeaderLogin: {written}}, capability, 0)
	switch {
	case err != nil && written != login:
		t.Errorf("serve's header %q for the login %q refused: %v", written, login, err)
	case err == nil && s.Login != login && (written != login || !encoded(s.Login)):
		t.Errorf("serve's header %q for the login %q read as %q", written, login, s.Login)
	}
}

// encoded is whether serve writes s as an encoded word rather than as it is:
// a byte that is not ASCII, and a control character, which is ASCII, alike.
func encoded(s string) bool {
	return mime.QEncoding.Encode("utf-8", s) != s
}

// keysWritten is how many keys the objects of the JSON value in d are written
// with, at every depth, each key counted as often as it is written.
func keysWritten(d *json.Decoder) (int, error) {
	tok, err := d.Token()
	if err != nil {
		return 0, err
	}
	n := 0
	switch tok {
	case json.Delim('{'):
		for d.More() {
			if _, err := d.Token(); err != nil {
				return 0, err
			}
			k, err := keysWritten(d)
			if err != nil {
				return 0, err
			}
			n += 1 + k
		}
	case json.Delim('['):
		for d.More() {
			k, err := keysWritten(d)
			if err != nil {
				return 0, err
			}
			n += k
		}
	default:
		return 0, nil
	}
	_, err = d.Token()
	return n, err
}

// entries is how many keys the objects of v, a value encoding/json decoded,
// hold at every depth: a key written twice is held once.
func entries(v any) int {
	n := 0
	switch v := v.(type) {
	case map[string]any:
		for _, e := range v {
			n += 1 + entries(e)
		}
	case []any:
		for _, e := range v {
			n += entries(e)
		}
	}
	return n
}
