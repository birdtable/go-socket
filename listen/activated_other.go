//go:build !linux && !darwin

package listen

// Activated is ErrNotActivated off Linux and macOS, where neither systemd nor
// launchd passes a socket: the environment is not read and names are not
// looked at, so variables a parent set by mistake take nothing.
func Activated(names ...string) ([]*Listener, error) { return nil, ErrNotActivated }
