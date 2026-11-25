//go:build unix

package second_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/birdtable/go-socket/listen"
)

// The shop's kiosk is the one program that listens at press.sock, so the
// socket a crash leaves there is the shop's to take back: it gives
// ReplaceEvenIfBusy, its own call, which Listen makes for no program on any
// system. Without it, every system refuses the socket
// with ErrMaybeBusy and leaves it. Windows has no socket to take back: the
// kiosk's pipe goes when its process does.
//
// What it costs when this fails: a shop whose kiosk crashed cannot open again
// until somebody removes its socket, or a program that never said so takes a
// socket that may be a busy one's.
func TestTheShopTakesBackTheSocketItsCrashLeft(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "fernhill")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "press.sock")
	var lc net.ListenConfig
	crashed, err := lc.Listen(ctx, "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	crashed.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := crashed.Close(); err != nil {
		t.Fatal(err)
	}
	if l, err := listen.Listen(ctx, sock); !errors.Is(err, listen.ErrMaybeBusy) {
		if err == nil {
			_ = l.Close()
		}
		t.Fatalf("the crash's socket with no word from the shop: %v, and want ErrMaybeBusy", err)
	}
	l, err := listen.Listen(ctx, sock, listen.ReplaceEvenIfBusy())
	if err != nil {
		t.Fatalf("the shop did not take back the socket its crash left: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}
