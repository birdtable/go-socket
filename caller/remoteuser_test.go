package caller

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// On the proxy's own listener, one user and a list of groups on the separator are read,
// as HTTP reads a list: the spaces and tabs around each taken off and empty ones
// dropped; an empty or absent user is nobody. Nothing else changes: a capital is kept.
//
// What it costs when this fails: a person the proxy logged in is refused,
// holds a group named " family" no list matches, a request with no user
// is read as somebody, or "Sam" is read as sam, which the program alone says.
func TestRemoteUserReadsTheProxysUserAndGroups(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		h      http.Header
		want   Remote
		groups []string
	}{
		{"a user and groups", http.Header{authUser: {" sam "}, authGroups: {"family, garden,,"}}, Remote{User: "sam"}, []string{"family", "garden"}},
		{"tabs around", http.Header{authUser: {"\tSam\t"}, authGroups: {"\tFamily\t,\t"}}, Remote{User: "Sam"}, []string{"Family"}},
		{"a user alone", http.Header{authUser: {"sam"}}, Remote{User: "sam"}, nil},
		{"groups alone", http.Header{authGroups: {"family"}}, Remote{}, nil},
		{"an empty user", http.Header{authUser: {"  "}, authGroups: {"family"}}, Remote{}, nil},
		{"no headers", http.Header{}, Remote{}, nil},
	}
	for _, c := range cases {
		got, err := authProxy().RemoteUser(c.h, true)
		if err != nil || got.User != c.want.User || !slices.Equal(got.Groups, c.groups) {
			t.Errorf("%s: %+v %v, want %q in %q", c.name, got, err, c.want.User, c.groups)
		}
	}
	if len(cases) != 6 {
		t.Fatalf("held %d of 6 requests", len(cases))
	}
}

// Either header written twice is an error, and on a listener that is not the
// proxy's nobody is believed, whatever the headers say.
//
// What it costs when this fails: a visitor's own user header, passed on
// beside the proxy's, is read as who they are, or anybody who reaches
// another listener names themselves.
func TestRemoteUserBelievesOnlyTheProxyWritingOnce(t *testing.T) {
	t.Parallel()
	refused := 0
	for name, h := range map[string]http.Header{
		"the user twice":            {authUser: {"sam", "admin"}},
		"the groups twice":          {authUser: {"sam"}, authGroups: {"family", "admins"}},
		"the groups twice, no user": {authGroups: {"family", "admins"}},
	} {
		if got, err := authProxy().RemoteUser(h, true); err == nil || got.User != "" || got.Groups != nil {
			t.Errorf("%s: %+v %v", name, got, err)
		}
		refused++
	}
	if refused != 3 {
		t.Fatalf("held %d of 3 requests", refused)
	}
	h := http.Header{authUser: {"sam"}, authGroups: {"admins"}}
	if got, err := authProxy().RemoteUser(h, false); err != nil || got.User != "" || got.Groups != nil {
		t.Errorf("on another listener: %+v %v", got, err)
	}
	h[authUser] = []string{"sam", "admin"}
	if got, err := authProxy().RemoteUser(h, false); err != nil || got.User != "" {
		t.Errorf("on another listener, the user twice: %+v %v", got, err)
	}
	if hs, err := authProxy().RemoteHeaders(); err != nil || !slices.Contains(hs, authUser) || !slices.Contains(hs, authGroups) {
		t.Errorf("RemoteHeaders leaves out a header RemoteUser reads: %q %v", hs, err)
	}
}

// A user or a group that is not UTF-8, or holds a control, format, line or
// paragraph separator character, is an error, never a caller.
//
// What it costs when this fails: a user whose name prints as another (a
// zero-width space, a right-to-left mark, a line separator, bytes that are
// not UTF-8) is read as somebody, and a page or a log shows them as another.
func TestRemoteUserRefusesANameThatPrintsAsAnother(t *testing.T) {
	t.Parallel()
	refused := 0
	for name, h := range map[string]http.Header{
		"a zero-width space":     {authUser: {"sa\u200bm"}},
		"a right-to-left mark":   {authUser: {"\u202emas"}},
		"a line separator":       {authUser: {"sam\u2028admin"}},
		"a user not UTF-8":       {authUser: {"sam\xffadmin"}},
		"a control in a group":   {authUser: {"sam"}, authGroups: {"family,adm\x00ins"}},
		"a format in a group":    {authUser: {"sam"}, authGroups: {"adm\u200bins"}},
		"a group not UTF-8":      {authUser: {"sam"}, authGroups: {"family,\xff"}},
		"a paragraph in a group": {authUser: {"sam"}, authGroups: {"a\u2029b"}},
	} {
		if got, err := authProxy().RemoteUser(h, true); err == nil || got.User != "" || got.Groups != nil {
			t.Errorf("%s: %+v %v", name, got, err)
		}
		refused++
	}
	if refused != 8 {
		t.Fatalf("held %d of 8 requests", refused)
	}
}

// A user or a group that starts or ends with a space other than the space and
// the tab is an error, never a caller: HTTP takes off those two alone, and
// which names are one is the program's to say.
//
// What it costs when this fails: "sam " is read as sam, so a name the
// identity provider let somebody choose takes sam's roles; or it is read as
// itself, and prints as sam on a page or in a log.
func TestRemoteUserRefusesANameWithAnotherSpaceAtAnEnd(t *testing.T) {
	t.Parallel()
	refused := 0
	for name, h := range map[string]http.Header{
		"a no-break space after a user":  {authUser: {"sam "}},
		"an ideographic space before":    {authUser: {"　sam"}},
		"a user of no-break spaces":      {authUser: {"  "}},
		"an en space after a group":      {authUser: {"sam"}, authGroups: {"family,admins "}},
		"a narrow no-break space before": {authUser: {"sam"}, authGroups: {" admins"}},
	} {
		if got, err := authProxy().RemoteUser(h, true); err == nil || got.User != "" || got.Groups != nil {
			t.Errorf("%s: %+v %v, want an error", name, got, err)
		}
		refused++
	}
	if refused != 5 {
		t.Fatalf("held %d of 5 requests", refused)
	}
	// The negative half: such a space within a name is the name's.
	h := http.Header{authUser: {"sam lee"}, authGroups: {"family room"}}
	if got, err := authProxy().RemoteUser(h, true); err != nil || got.User != "sam lee" || !slices.Equal(got.Groups, []string{"family room"}) {
		t.Errorf("a no-break space within: %+v %v, want it kept", got, err)
	}
}

// The headers a login proxy of the tests writes: oauth2-proxy's, not the
// Remote- pair, so nothing here reads a name the package once held.
const (
	authUser   = "X-Auth-Request-User"
	authGroups = "X-Auth-Request-Groups"
)

// authProxy is a login proxy that writes authUser and authGroups, its groups
// separated by commas.
func authProxy() LoginProxy { return LoginProxy{User: authUser, Groups: authGroups, Separator: ','} }

// A login proxy's two names are HTTP tokens and two headers, and given: one
// that is empty, is not a token, or names the other in another case is an
// error, on the proxy's listener and off it, and never a caller; and
// RemoteHeaders, which a program strips by, is the same error and no names.
//
// What it costs when this fails: a program that left a name out, or wrote
// one twice, reads its users from a header nobody sets, or from one that
// holds the groups, and names a visitor by what they sent; or strips by a
// list that leaves the proxy's user header in.
func TestALoginProxyNamesTwoHeadersOfItsOwn(t *testing.T) {
	t.Parallel()
	h := http.Header{authUser: {"sam"}, authGroups: {"family"}}
	refused := 0
	for name, p := range map[string]LoginProxy{
		"no names":                   {},
		"no groups header":           {User: authUser, Separator: ','},
		"no user header":             {Groups: authGroups, Separator: ','},
		"a user name with a space":   {User: "X-Auth User", Groups: authGroups, Separator: ','},
		"a groups name with a colon": {User: authUser, Groups: "X-Auth-Groups:", Separator: ','},
		"a name not ASCII":           {User: "X-Utilisateur-Connecté", Groups: authGroups, Separator: ','},
		"one header twice":           {User: authUser, Groups: authUser, Separator: ','},
		"one header in another case": {User: authUser, Groups: "x-auth-request-user", Separator: ','},
	} {
		for _, fromProxy := range []bool{true, false} {
			if got, err := p.RemoteUser(h, fromProxy); err == nil || got.User != "" || got.Groups != nil {
				t.Errorf("%s, fromProxy %v: %+v %v, want an error", name, fromProxy, got, err)
			}
		}
		if hs, err := p.RemoteHeaders(); err == nil || hs != nil {
			t.Errorf("%s: RemoteHeaders is %q %v, want an error and no names", name, hs, err)
		}
		refused++
	}
	if refused != 8 {
		t.Fatalf("held %d of 8 proxies", refused)
	}
	// A name left out is refused as left out, not only as no token: the
	// error tells the program there is no default to fall back on.
	for _, p := range []LoginProxy{{}, {User: authUser, Separator: ','}, {Groups: authGroups, Separator: ','}} {
		if _, err := p.RemoteUser(h, true); err == nil || !strings.Contains(err.Error(), "there is no default") {
			t.Errorf("%+v: %v, want an error that says there is no default", p, err)
		}
	}
	// The negative half: names of the program's own, in any case, are read,
	// and the Remote- pair is just another two names.
	for _, p := range []LoginProxy{authProxy(), {User: "x-auth-request-user", Groups: "X-AUTH-REQUEST-GROUPS", Separator: ','}} {
		if got, err := p.RemoteUser(h, true); err != nil || got.User != "sam" || !slices.Equal(got.Groups, []string{"family"}) {
			t.Errorf("%+v: %+v %v, want sam in family", p, got, err)
		}
		if hs, err := p.RemoteHeaders(); err != nil || !slices.Equal(hs, []string{authUser, authGroups}) {
			t.Errorf("%+v: RemoteHeaders is %q %v, want the two it reads", p, hs, err)
		}
	}
	remote := LoginProxy{User: "Remote-User", Groups: "Remote-Groups", Separator: ','}
	if got, err := remote.RemoteUser(h, true); err != nil || got.User != "" {
		t.Errorf("the Remote- pair read %+v %v from another proxy's headers", got, err)
	}
}

// The character between two groups is the program's, as the names are: a
// header separated by pipes is read by a '|', and a group's name may hold
// the other proxy's separator. A separator left out, or one a group's name
// holds (a letter, a digit, a space, a token's other marks, a control, a
// character not ASCII), is an error on the proxy's listener and off it, and
// in RemoteHeaders.
//
// What it costs when this fails: behind a proxy that writes "family|garden",
// a member of family is in no group the program knows, or a group named
// "family, pantry" makes its members members of pantry, which nobody
// granted them.
func TestALoginProxyNamesItsSeparator(t *testing.T) {
	t.Parallel()
	piped := LoginProxy{User: authUser, Groups: authGroups, Separator: '|'}
	semi := LoginProxy{User: authUser, Groups: authGroups, Separator: ';'}
	for _, c := range []struct {
		p      LoginProxy
		groups string
		want   []string
	}{
		{piped, "family|garden", []string{"family", "garden"}},
		{authProxy(), "family|garden", []string{"family|garden"}},
		{piped, "family, pantry| admins |", []string{"family, pantry", "admins"}},
		{authProxy(), "family, pantry|admins", []string{"family", "pantry|admins"}},
		{semi, "family;garden, shed", []string{"family", "garden, shed"}},
	} {
		got, err := c.p.RemoteUser(http.Header{authUser: {"sam"}, authGroups: {c.groups}}, true)
		if err != nil || got.User != "sam" || !slices.Equal(got.Groups, c.want) {
			t.Errorf("%q split on %q: %+v %v, want sam in %q", c.groups, c.p.Separator, got, err, c.want)
		}
	}
	h := http.Header{authUser: {"sam"}, authGroups: {"family"}}
	refused := 0
	for _, sep := range []rune{0, 'a', 'Z', '7', ' ', '\t', '\n', 0x7f, '-', '.', '_', '~', '!', '¦', '｜'} {
		p := LoginProxy{User: authUser, Groups: authGroups, Separator: sep}
		for _, fromProxy := range []bool{true, false} {
			if got, err := p.RemoteUser(h, fromProxy); err == nil || !strings.Contains(err.Error(), "no separator") || got.User != "" || got.Groups != nil {
				t.Errorf("a separator of %q, fromProxy %v: %+v %v, want it refused as no separator", sep, fromProxy, got, err)
			}
		}
		if hs, err := p.RemoteHeaders(); err == nil || hs != nil {
			t.Errorf("a separator of %q: RemoteHeaders is %q %v, want an error and no names", sep, hs, err)
		}
		refused++
	}
	if refused != 15 {
		t.Fatalf("held %d of 15 separators", refused)
	}
}

// A proxy that writes no groups header is said to with NoGroups: no groups
// header is read, whatever a visitor sends, and RemoteHeaders is the user's
// alone. NoGroups beside a groups header or a separator is an error, and a
// groups header left out without it stays one.
//
// What it costs when this fails: behind a proxy that writes a user alone, the
// program names a groups header the proxy never overwrites, and a logged-in
// visitor's own "X-Groups: admins" makes them an admin.
func TestALoginProxyThatWritesNoGroups(t *testing.T) {
	t.Parallel()
	const email = "X-Goog-Authenticated-User-Email"
	iap := LoginProxy{User: email, NoGroups: true}
	h := http.Header{email: {"sam@example.com"}, "X-Groups": {"admins"}, authGroups: {"admins"}}
	if got, err := iap.RemoteUser(h, true); err != nil || got.User != "sam@example.com" || got.Groups != nil {
		t.Errorf("a proxy of no groups, and a visitor's own groups headers: %+v %v, want sam@example.com in no group", got, err)
	}
	if hs, err := iap.RemoteHeaders(); err != nil || !slices.Equal(hs, []string{email}) {
		t.Errorf("a proxy of no groups: RemoteHeaders is %q %v, want the user's alone", hs, err)
	}
	h.Add(email, "admin@example.com")
	if got, err := iap.RemoteUser(h, true); err == nil || got.User != "" {
		t.Errorf("a proxy of no groups, the user twice: %+v %v, want an error", got, err)
	}
	refused := 0
	for name, p := range map[string]LoginProxy{
		"no groups beside a groups header": {User: email, Groups: "X-Groups", NoGroups: true},
		"no groups beside a separator":     {User: email, Separator: ',', NoGroups: true},
		"no groups and no user header":     {NoGroups: true},
		"no groups, a user not a token":    {User: "X-Goog Email", NoGroups: true},
		"a groups header left out":         {User: email, Separator: ','},
	} {
		for _, fromProxy := range []bool{true, false} {
			if got, err := p.RemoteUser(http.Header{email: {"sam@example.com"}}, fromProxy); err == nil || got.User != "" {
				t.Errorf("%s, fromProxy %v: %+v %v, want an error", name, fromProxy, got, err)
			}
		}
		if hs, err := p.RemoteHeaders(); err == nil || hs != nil {
			t.Errorf("%s: RemoteHeaders is %q %v, want an error and no names", name, hs, err)
		}
		refused++
	}
	if refused != 5 {
		t.Fatalf("held %d of 5 proxies", refused)
	}
}
