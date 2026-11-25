package peer

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// agentAt is an environment whose SSH_AUTH_SOCK is v, and with nothing else.
func agentAt(v string) func(string) string {
	return func(k string) string {
		if k == agentEnv {
			return v
		}
		return ""
	}
}

// DialAgent with no env to read is refused, and nothing is dialled.
//
// What it costs when this fails: a program that passes no env panics, or
// reads an environment it did not choose.
func TestDialAgentWithNoEnvIsRefused(t *testing.T) {
	t.Parallel()
	if c, err := DialAgent(t.Context(), nil); !errors.Is(err, fs.ErrInvalid) || c != nil {
		t.Errorf("DialAgent with a nil env = %v, %v, and want fs.ErrInvalid", c, err)
	}
}

// DialAgent gives a ctx with no deadline 5 seconds for the dial, as its doc
// says: the tests wait out a bound of their own, so this row holds the
// product's.
//
// What it costs when this fails: a product's dial with no deadline gives up
// on a slow agent too soon, or waits on a name held and never served longer
// than the doc says.
func TestDialAgentsBoundIsFiveSeconds(t *testing.T) {
	t.Parallel()
	if agentDialBound != 5*time.Second {
		t.Errorf("DialAgent's bound for a ctx with no deadline is %v, and its doc says 5s", agentDialBound)
	}
}

// An agent's pipe is taken only when its owner is this process's own user,
// SYSTEM or BUILTIN\Administrators, each spelt exactly as Windows writes it;
// every other owner, nobody's among them, is refused with fs.ErrPermission
// and named in the error.
//
// What it costs when this fails: a product on Windows sends its keys, its
// passphrase and its requests to sign to a pipe another account made at the
// agent's name, or refuses the real agent.
func TestAnAgentsPipeIsTakenOnlyFromThisAccountSystemOrAdministrators(t *testing.T) {
	t.Parallel()
	const me = "S-1-5-21-1-2-3-1001"
	for _, c := range []struct {
		owner, user string
		taken       bool
	}{
		{me, me, true},
		{"S-1-5-18", me, true},
		{"S-1-5-32-544", me, true},
		{"S-1-5-21-1-2-3-1002", me, false},
		{"S-1-5-21-9-9-9-1001", me, false},
		{"S-1-1-0", me, false},
		{"S-1-5-11", me, false},
		{"S-1-5-32-545", me, false},
		{"S-1-5-19", me, false},
		{"S-1-5-20", me, false},
		{"S-1-5-18-1", me, false},
		{"s-1-5-18", me, false},
		{"S-1-5-32-544 ", me, false},
		{"", me, false},
		{"", "", false},
		{me, "", false},
	} {
		err := agentOwner(c.owner, c.user)
		if c.taken {
			if err != nil {
				t.Errorf("a pipe owned by %q, for %q: %v, and want it taken", c.owner, c.user, err)
			}
			continue
		}
		if !errors.Is(err, errAgentOwner) || !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), fmt.Sprintf("%q", c.owner)) {
			t.Errorf("a pipe owned by %q, for %q: %v, and want it refused with fs.ErrPermission, naming the owner", c.owner, c.user, err)
		}
	}
}
