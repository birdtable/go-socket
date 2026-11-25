// Package second is go-socket's second consumer: a print shop's kiosk, Fernhill, that takes
// print jobs over a socket, built on values deliberately unlike any other program's. Its socket
// mode, its group, its spans, its badge reader's headers and separator, its body limit, its
// kinds of answer, its kinds of printer, its grant and its Tailscale floor are all its own. It is
// its tests alone; the pre-commit tier runs them on every system, so a value a library fixes
// that such a program would set otherwise fails here first.
package second
