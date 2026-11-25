//go:build linux || darwin || freebsd || windows

package second_test

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/birdtable/go-socket/caller"
	"github.com/birdtable/go-socket/listen"
)

// The shop's own values, each unlike ours.
const (
	// bodyCap is the shop's own limit on a request's body and on an
	// answer it reads: a job carries a whole document.
	bodyCap = 256 << 10
	// docSize is a document the shop prints, well inside its limit.
	docSize = 100 << 10
	// press is the capability the shop's grants come under.
	press = "fernhill.example/cap/press"
	// floorMajor.floorMinor is the oldest tailscaled the shop runs behind.
	floorMajor, floorMinor = 1, 98
	// grantsCap is over caller.MaxCaps: a person's grants name every press
	// in every shop of the chain.
	grantsCap = 192 << 10
)

// The shop's kinds of answer that could not be done: its own words.
const (
	// turnedAway is a job the shop will not take: no badge, no right, no job.
	turnedAway = "turned-away"
	// queued is a job that waits for a press not on this machine.
	queued = "queued"
	// jammed is a job the press took and could not print.
	jammed = "jammed"
)

// problem is the shop's answer that a job could not be done, of one of its
// kinds, with a hint the kiosk shows.
type problem struct {
	Kind string `json:"kind"`
	Msg  string `json:"msg"`
	Hint string `json:"hint,omitempty"`
}

// refusal is a problem of the shop's kind, in format's words.
func refusal(kind, format string, a ...any) *problem {
	return &problem{Kind: kind, Msg: fmt.Sprintf(format, a...)}
}

// badges is the shop's own login proxy, the badge reader at its door: its own
// names for who is asking and what they may do, and its own separator
// between two rights.
func badges() caller.LoginProxy {
	return caller.LoginProxy{User: "Fernhill-Badge", Groups: "Fernhill-Rights", Separator: ';'}
}

// settings is what the shop opens a printer with.
type settings struct{ Tray string }

// printer is what the shop's table of presses opens.
type printer interface {
	Print(doc []byte) string
}

// laser is the one printer this machine has.
type laser struct{ tray string }

func (l laser) Print(doc []byte) string {
	return "tray " + l.tray + ": " + strconv.Itoa(len(doc)) + " bytes"
}

// errNotHere is a press the shop has, but not on this machine.
var errNotHere = errors.New("not on this machine")

// table is the shop's own table of presses, by kind; go-socket has none to
// lend it.
type table map[string]func(settings) (printer, error)

// open opens the press of kind with s.
func (p table) open(kind string, s settings) (printer, error) {
	open, ok := p[kind]
	if !ok {
		return nil, fmt.Errorf("press %q: the shop has none", kind)
	}
	return open(s)
}

// printers is the shop's presses: a laser here, a riso that is not on this
// machine, and a relay, the shop next door's press, which is closed.
func printers() table {
	return table{
		"laser": func(s settings) (printer, error) { return laser{tray: s.Tray}, nil },
		"riso":  func(settings) (printer, error) { return nil, fmt.Errorf("press %q: %w", "riso", errNotHere) },
		"relay": func(settings) (printer, error) { return nil, errors.New("next door is closed") },
	}
}

// job is a print job, the body of a request; printed is its answer, the
// body of the reply.
type job struct {
	ID      string `json:"id"`
	Printer string `json:"printer"`
	Doc     []byte `json:"doc"`
}

type printed struct {
	ID    string   `json:"id"`
	Said  string   `json:"said,omitempty"`
	Error *problem `json:"error,omitempty"`
}

// shop answers one job per request on plain net/http: the job is the JSON
// body, read strictly under the shop's own limit, and the answer is the
// JSON body of the reply. A body over the limit is a 413, before the job is
// read as one.
func shop(r table) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var j job
		err := json.UnmarshalRead(http.MaxBytesReader(w, req.Body, bodyCap), &j, json.RejectUnknownMembers(true))
		if _, over := errors.AsType[*http.MaxBytesError](err); over {
			http.Error(w, "the job is over the shop's limit", http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			reply(w, printed{Error: refusal(turnedAway, "not a job: %v", err)})
			return
		}
		out := printed{ID: j.ID}
		out.Error = func() *problem {
			// The shop's socket is reached by its badge reader alone, so
			// every request came through it.
			v, err := badges().RemoteUser(req.Header, true)
			switch {
			case err != nil:
				return refusal(turnedAway, "no badge: %v", err)
			case v.User == "":
				return refusal(turnedAway, "no badge: nobody is named in Fernhill-Badge")
			}
			if !slices.Contains(v.Groups, "print") {
				e := refusal(turnedAway, "%s may not print", v.User)
				e.Hint = "ask at the counter"
				return e
			}
			p, err := r.open(j.Printer, settings{Tray: "A3"})
			if errors.Is(err, errNotHere) {
				return refusal(queued, "%v", err)
			}
			if err != nil {
				return refusal(jammed, "%v", err)
			}
			out.Said = p.Print(j.Doc)
			return nil
		}()
		reply(w, out)
	})
}

// reply writes a as the shop's answer.
func reply(w http.ResponseWriter, a printed) {
	b, err := json.Marshal(a)
	if err != nil {
		http.Error(w, "the shop could not answer: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b) // an error here is the asker gone, with no one left to tell
}

// ask sends j as v, through the badge reader, and reads the answer: the JSON
// body when the shop answers 200, read strictly under the shop's limit, and
// the shop's own words as an error when it answers anything else.
func ask(t *testing.T, ctx context.Context, c *http.Client, v caller.Remote, j job) (printed, error) {
	t.Helper()
	body, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://fernhill/print", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	// The badge reader writes its two headers, as caller names them.
	names, err := badges().RemoteHeaders()
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(names[0], v.User)
	req.Header.Set(names[1], strings.Join(v.Groups, ";"))
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		said, err := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		if err != nil {
			t.Fatal(err)
		}
		return printed{}, fmt.Errorf("the shop answered %s: %s", res.Status, said)
	}
	var a printed
	if err := json.UnmarshalRead(io.LimitReader(res.Body, bodyCap), &a, json.RejectUnknownMembers(true)); err != nil {
		t.Fatal(err)
	}
	return a, nil
}

// The shop serves its own socket, on its own spans, behind its own peer
// check, or on Windows its own access list naming its kiosk's account, and
// takes jobs through its own badge reader, as JSON bodies under its own
// limit, from a table of its own printers, with answers of its own kinds.
//
// What it costs when this fails: a value one of the libraries fixes cannot
// be set by a program that is not ours, and the rule that a value is the caller's holds only for the
// programs we wrote.
func TestAPrintShopServesOnValuesOfItsOwn(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	l, dial := endpoint(t, ctx)

	o := listen.Options{
		Idle:              listen.NewIdle(90*time.Second, 250*time.Millisecond),
		Grace:             750 * time.Millisecond,
		ReadHeaderTimeout: 3 * time.Second,
		KeepAlive:         45 * time.Second,
	}
	sctx, stop := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- listen.Serve(sctx, []*listen.Listener{l}, listen.Same(shop(printers())), o) }()

	c := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
	}}
	doc := bytes.Repeat([]byte("f"), docSize)
	ines := caller.Remote{User: "ines", Groups: []string{"print", "staple"}}

	got, err := ask(t, ctx, c, ines, job{ID: "j1", Printer: "laser", Doc: doc})
	if err != nil || got.Error != nil || got.ID != "j1" || !strings.Contains(got.Said, "A3") {
		t.Fatalf("a job the badge may print was not printed: %+v %v", got, err)
	}

	got, err = ask(t, ctx, c, caller.Remote{User: "otto", Groups: []string{"staple"}}, job{ID: "j2", Printer: "laser", Doc: doc})
	if err != nil || got.Error == nil || got.Error.Kind != turnedAway || got.Error.Hint != "ask at the counter" {
		t.Fatalf("a badge that may not print was not turned away, with the shop's hint: %+v %v", got.Error, err)
	}

	got, err = ask(t, ctx, c, ines, job{ID: "j3", Printer: "riso", Doc: []byte("x")})
	if err != nil || got.Error == nil || got.Error.Kind != queued || !strings.Contains(got.Error.Msg, `press "riso"`) {
		t.Fatalf("a printer not on this machine was not said to be queued, in the shop's noun: %+v %v", got.Error, err)
	}

	got, err = ask(t, ctx, c, ines, job{ID: "j4", Printer: "relay", Doc: []byte("x")})
	if err != nil || got.Error == nil || got.Error.Kind != jammed || got.Error.Msg != "next door is closed" {
		t.Fatalf("a printer that could not open was not said to be jammed: %+v %v", got.Error, err)
	}

	// A document as long as the shop's limit is a body over it, refused
	// as such: the limit is the shop's, set on its own handler.
	_, err = ask(t, ctx, c, ines, job{ID: "j5", Printer: "laser", Doc: bytes.Repeat([]byte("f"), bodyCap)})
	if err == nil || !strings.Contains(err.Error(), "413 Request Entity Too Large: the job is over the shop's limit") {
		t.Fatalf("a job over the shop's limit of %d bytes: %v, want the shop's 413", bodyCap, err)
	}

	stop()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

// The shop reads a job strictly: a member it does not name, or a name
// written twice, is no job, and is turned away in the shop's words.
//
// What it costs when this fails: a kiosk reads a job it never wrote, a
// colour or a priority it has no field for, and prints it anyway.
func TestTheShopReadsAJobStrictly(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"id":"j6","printer":"laser","doc":"","colour":true}`,
		`{"id":"j7","printer":"laser","printer":"riso","doc":""}`,
	} {
		rec := httptest.NewRecorder()
		shop(printers()).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/print", strings.NewReader(body)))
		var a printed
		if err := json.Unmarshal(rec.Body.Bytes(), &a); rec.Code != http.StatusOK || err != nil || a.Error == nil || a.Error.Kind != turnedAway || !strings.HasPrefix(a.Error.Msg, "not a job: ") {
			t.Errorf("%s: %d %+v %v, want it turned away as not a job", body, rec.Code, a.Error, err)
		}
	}
}

// grant is the shop's own grant: the presses a person may run, and whether
// at night. No role, and no permission name.
type grant struct {
	Presses []string `json:"presses"`
	Nightly bool     `json:"nightly"`
}

// presses is the presses the grants under the shop's capability give, read
// as the shop reads them: each grant strictly as a grant, and a press given
// twice refused, not merged, as a grant written twice is no grant the shop
// issued. Nightly is whether any grant gives the night.
func presses(values []jsontext.Value) (names []string, nightly bool, err error) {
	for _, v := range values {
		var g grant
		if err := json.Unmarshal(v, &g, json.RejectUnknownMembers(true)); err != nil {
			return nil, false, err
		}
		for _, p := range g.Presses {
			if slices.Contains(names, p) {
				return nil, false, fmt.Errorf("press %q is granted twice", p)
			}
			names = append(names, p)
		}
		nightly = nightly || g.Nightly
	}
	return names, nightly, nil
}

// serveFloor is the version below which tailscale serve passes a caller's own
// grants header on as if it wrote it: the fact ParseVersion's doc gives. The
// shop's floor is its own, and never under this.
const serveFloorMajor, serveFloorMinor = 1, 92

// The shop reads tailscale serve's grant under its own capability with a
// type of its own, refusing a press granted twice, under a cap of its own
// over caller.MaxCaps; holds tailscaled to a floor of its own, never under
// serve's 1.92; and reads a login proxy's user from headers of the shop's own
// naming.
//
// What it costs when this fails: a grant caller does not know the shape of
// is refused or reshaped, grants longer than MaxCaps cannot be read, a floor
// that is not the shop's is held, or one under 1.92 lets a caller write its
// own grants, or a proxy's user is read from headers the shop does not name,
// for a program that is not ours.
func TestThePressGrantAndFloorAreTheShops(t *testing.T) {
	t.Parallel()
	readUnder := func(caps string, limit int) ([]string, bool, error) {
		h := http.Header{}
		h.Set(caller.HeaderLogin, "ines@fernhill.example")
		h.Set(caller.HeaderCaps, caps)
		s, err := caller.TailscaleServe(h, press, limit)
		if err != nil {
			return nil, false, err
		}
		return presses(s.Values)
	}
	read := func(caps string) ([]string, bool, error) { return readUnder(caps, grantsCap) }
	names, nightly, err := read(`{"` + press + `":[{"presses":["east","west"],"nightly":true},{"presses":["north"]}]}`)
	if err != nil || !nightly || !slices.Equal(names, []string{"east", "west", "north"}) {
		t.Fatalf("the shop's grants: %v %v %v", names, nightly, err)
	}
	if _, _, err := read(`{"` + press + `":[{"presses":["east"]},{"presses":["east"]}]}`); err == nil {
		t.Error("a press granted twice was read, and want it refused")
	}
	if _, _, err := read(`{"` + press + `":[{"presses":["east"],"role":"admin"}]}`); err == nil {
		t.Error("a grant with a member the shop does not name was read, and want it refused")
	}

	// The chain's grants run past caller.MaxCaps, and are read under the
	// shop's own cap; 0 still holds them to MaxCaps, and a cap below 0 is no
	// cap.
	var chain []string
	for i := 0; len(strings.Join(chain, `","`)) <= caller.MaxCaps; i++ {
		chain = append(chain, fmt.Sprintf("shop-%04d-press-%04d", i/16, i%16))
	}
	long := `{"` + press + `":[{"presses":["` + strings.Join(chain, `","`) + `"]}]}`
	if len(long) <= caller.MaxCaps || len(long) > grantsCap {
		t.Fatalf("the chain's grants are %d bytes, want over %d and at most %d", len(long), caller.MaxCaps, grantsCap)
	}
	if names, _, err := read(long); err != nil || len(names) != len(chain) {
		t.Errorf("the chain's grants of %d bytes under the shop's cap of %d: %d presses, %v, want %d", len(long), grantsCap, len(names), err, len(chain))
	}
	if _, _, err := readUnder(long, 0); !errors.Is(err, caller.ErrTooLong) {
		t.Errorf("the chain's grants under a cap of 0: %v, want caller.ErrTooLong, as 0 holds them to MaxCaps", err)
	}
	if _, _, err := readUnder(`{"`+press+`":[{"presses":["east"]}]}`, -1); err == nil {
		t.Error("a cap of -1 was taken, and want it refused")
	}

	if floorMajor < serveFloorMajor || floorMajor == serveFloorMajor && floorMinor < serveFloorMinor {
		t.Fatalf("the shop's floor %d.%d is under %d.%d, below which serve passes a caller's own grants on", floorMajor, floorMinor, serveFloorMajor, serveFloorMinor)
	}
	floor := func(version string) bool {
		major, minor, ok := caller.ParseVersion(version)
		return ok && (major > floorMajor || major == floorMajor && minor >= floorMinor)
	}
	for version, want := range map[string]bool{
		"1.98.0": true, "1.98.2-t0123abcd-g4567ef": true, "2.1.0": true,
		"1.97.9": false, "1.96": false, "1.91.4": false, "+1.99": false, "1": false,
	} {
		if floor(version) != want {
			t.Errorf("tailscaled %s against the shop's floor: %v, want %v", version, !want, want)
		}
	}

	// The shop's login proxy writes its user and groups under names of its
	// own, which are neither Remote-User nor Remote-Groups, and its groups
	// between pipes, not commas; a name that is not a
	// header's, two names of one header, and a separator a group's name
	// holds are refused, and RemoteHeaders refuses them too.
	door := caller.LoginProxy{User: "X-Door-Clerk", Groups: "X-Door-Benches", Separator: '|'}
	h := http.Header{}
	h.Set("X-Door-Clerk", "Ines")
	h.Set("X-Door-Benches", "counter | press, back")
	h.Set("Remote-User", "mallory")
	r, err := door.RemoteUser(h, true)
	if err != nil || r.User != "Ines" || !slices.Equal(r.Groups, []string{"counter", "press, back"}) {
		t.Fatalf("the proxy's user: %+v %v", r, err)
	}
	if got, err := door.RemoteHeaders(); err != nil || !slices.Equal(got, []string{"X-Door-Clerk", "X-Door-Benches"}) {
		t.Errorf("the proxy's headers to strip: %q %v, and want the shop's two", got, err)
	}
	for _, bad := range []caller.LoginProxy{
		{User: "X-Door Clerk", Groups: "X-Door-Benches", Separator: '|'},
		{User: "X-Door-Clerk", Groups: "x-door-clerk", Separator: '|'},
		{User: "X-Door-Clerk", Groups: "X-Door-Benches", Separator: '-'},
		{User: "X-Door-Clerk", Groups: "X-Door-Benches"},
	} {
		if r, err := bad.RemoteUser(h, true); err == nil {
			t.Errorf("a proxy named %q and %q split on %q read %+v, and want it refused", bad.User, bad.Groups, bad.Separator, r)
		}
		if got, err := bad.RemoteHeaders(); err == nil {
			t.Errorf("a proxy named %q and %q split on %q strips %q, and want it refused", bad.User, bad.Groups, bad.Separator, got)
		}
	}
}
