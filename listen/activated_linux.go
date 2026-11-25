package listen

import (
	"fmt"
	"os"

	"github.com/coreos/go-systemd/v22/activation"
)

// Activated is the listeners systemd passed this process, each with the name
// the unit gave it (Name): with no names, every one in the socket unit's
// order; with names, those passed under each name, in the order named, and
// the rest are closed. The variables that passed them are taken out of the
// environment, so a program this one starts is not handed them too. A
// process systemd did not start by its socket, or passed nothing under one of
// names, is ErrNotActivated; one passed a descriptor that is not a stream
// socket listening is refused. A name that is empty or named twice is refused
// with fs.ErrInvalid, before anything is read. On every refusal each
// descriptor passed is closed.
func Activated(names ...string) ([]*Listener, error) {
	if err := distinct(names); err != nil {
		return nil, err
	}
	files := activation.Files(true)
	if len(files) == 0 {
		return nil, ErrNotActivated
	}
	if len(names) > 0 {
		var err error
		if files, err = named(files, names); err != nil {
			return nil, err
		}
	}
	return listenersOf(files, (*os.File).Name, "the unit says ListenStream= for each")
}

// named is the files passed under each of names, in the order named, each
// name's in the unit's order, and closes the rest. A name nothing was passed
// under is ErrNotActivated, and then every file is closed.
func named(files []*os.File, names []string) ([]*os.File, error) {
	picked := make([]*os.File, 0, len(files))
	taken := make(map[*os.File]bool, len(files))
	for _, n := range names {
		before := len(picked)
		for _, f := range files {
			if f.Name() == n {
				picked, taken[f] = append(picked, f), true
			}
		}
		if len(picked) == before {
			return nil, joined(fmt.Errorf("systemd passed no socket named %q: %w", n, ErrNotActivated), closeFiles(files))
		}
	}
	rest := make([]*os.File, 0, len(files)-len(picked))
	for _, f := range files {
		if !taken[f] {
			rest = append(rest, f)
		}
	}
	if err := closeFiles(rest); err != nil {
		return nil, joined(err, closeFiles(picked))
	}
	return picked, nil
}
