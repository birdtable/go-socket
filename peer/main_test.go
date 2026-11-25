package peer

import (
	"os"
	"testing"

	"github.com/birdtable/go-testkit"
)

// TestMain starts the tests from an environment of their own (testkit.Main), and panics when
// that environment cannot be made or put back, so the run fails naming why.
func TestMain(m *testing.M) {
	code, err := testkit.Main(m)
	if err != nil {
		panic(err)
	}
	os.Exit(code)
}

// What it costs when this fails: a test reads or writes the person's own home, temp folder or
// settings, or runs with an agent's or git's variables, and passes or fails on what the machine holds.
func TestTheTestsRunInAnEnvironmentOfTheirOwn(t *testing.T) {
	t.Parallel()
	testkit.Isolated(t)
}
