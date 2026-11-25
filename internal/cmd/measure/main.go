// Command measure prints the size of the module's code. The Taskfile runs it from the module's
// root:
//
//	measure size       prints the non-test Go lines and fails above the ceiling
//
// It counts as go-files' internal/cmd/measure does, so the two libraries' sizes read alike, with
// go-socket's own Ceiling.
package main

import "os"

func main() { os.Exit(run(os.Args[1:], ".", os.Stdout)) }
