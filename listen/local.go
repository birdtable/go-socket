package listen

import (
	"fmt"
	"strings"
)

// Local is the address of a listener on this machine named name, in folder:
// the string Listen takes for it on this system, and the one a client is
// told, as SSH_AUTH_SOCK is, since on each system they are the same.
//
// On Unix it is folder and name joined, made absolute as Listen makes a path.
// A path Listen would refuse for its length, its own or its socket's private
// name's, is refused here, with the error Listen gives, so a socket never
// moves to a shorter folder than the caller's.
//
// On Windows it is \\.\pipe\, name, a dash and the first 16 hex digits of the
// SHA-256 of folder made absolute: pipe names are the machine's, so two
// folders give two pipes, as they give two paths on Unix. Two spellings of
// one folder, in another case or through a link, give two pipes too.
//
// name is one plain name. It is refused, quoting it: empty, with
// ErrNoAddress; . or .., a / or \, or a NUL; and on Windows one that ends in
// a dot or a space, which Listen refuses in a pipe's name. folder is not made
// and not looked at beyond making it absolute: Listen does the rest. Local
// makes nothing.
func Local(folder, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("listen.Local(%q, %q): %w", folder, name, ErrNoAddress)
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", fmt.Errorf("listen.Local(%q, %q): the name is one plain name: not . or .., and with no / or \\ or NUL", folder, name)
	}
	addr, err := localAt(folder, name)
	if err != nil {
		return "", fmt.Errorf("listen.Local(%q, %q): %w", folder, name, err)
	}
	return addr, nil
}
