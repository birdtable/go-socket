package listen

import (
	"os"
	"testing"

	"github.com/birdtable/go-testkit"
)

// activatedChildEnv, set to 1, makes the test binary the child an activation test starts, as
// systemd would have started it.
const activatedChildEnv = "GO_SOCKET_ACTIVATED_CHILD"

// umaskChildEnv, set to 1, makes the test binary the child the mode test starts under a umask of
// 0, which only the shell that starts it sets.
const umaskChildEnv = "GO_SOCKET_UMASK_CHILD"

// TestMain starts the tests from an environment of their own (testkit.Main), panics when that
// environment cannot be made or put back, and fails the run when a goroutine is left blocked for
// good (testkit.NoLeaks). A process a test started as the listening child is that child first,
// and never returns from runChild; the children the activation and mode tests start run as they
// were handed, their parent's environment, already the tests' own, with what the parent added,
// which testkit.Main would drop.
func TestMain(m *testing.M) {
	runChild()
	if os.Getenv(activatedChildEnv) == "1" || os.Getenv(umaskChildEnv) == "1" {
		os.Exit(m.Run())
	}
	code, err := testkit.Main(m)
	if err != nil {
		panic(err)
	}
	os.Exit(testkit.NoLeaks(code))
}

// What it costs when this fails: a test reads or writes the person's own home, temp folder or
// settings, or runs with an agent's or git's variables, and passes or fails on what the machine holds.
func TestTheTestsRunInAnEnvironmentOfTheirOwn(t *testing.T) {
	t.Parallel()
	testkit.Isolated(t)
}
