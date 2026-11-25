// Command testexec runs one test binary for go test's -exec and records how it ended.
// The test member of Taskfile.yml builds it into build/ and passes it to go test,
// which then starts every test binary through it:
//
//	testexec <test binary> <its arguments>
//
// It passes its stdin to the binary, the binary's output through, and its exit code back to go
// test. A binary that exits 0 while a process it started holds its output open more than 5 s
// fails, with exit code 1, as go test fails it when it runs the binary itself. It records two lines
// for each binary, its start and its end, in the file GO_SOCKET_TEST_EXITS names. The end line
// gives the exit code in full, in hex, so a Windows code reads as itself: 0x00000001 is a test
// that failed or a process ended from outside, 0xC000013A a Ctrl-C or Ctrl-Break, 0xC0000005 a
// crash. Beside the code is the binary's last line of output. A test binary that ends by itself
// ends on its PASS or FAIL line, or a panic's stack; one ended from outside stops wherever its
// output was, mid-test. When the binary
// exits non-zero, testexec also prints the end line to its stderr, which go test reads as the
// package's output. A start line with no end line means testexec itself was ended, as go test
// ends it past its kill timeout; a kill of testexec does not reach the binary, which may then run
// on. The file is
// written line by line as each binary starts and ends, so it holds what happened even when go
// test, hatch or the json they write is cut off.
//
// It catches Ctrl-C and Ctrl-Break, and on a Unix SIGQUIT and SIGTERM, records each, and passes
// it to the binary, so it outlives them and records how the binary ended. On Windows, where a
// console's Ctrl event reaches every process on the console, the binary has its own already. A
// write of the binary's output that fails, as when go test is gone, is recorded on the end line,
// and the binary goes on.
//
// It imports the standard library alone and opens no socket.
package main

import (
	"context"
	"os"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv(exitsVar), os.Stdin, os.Stdout, os.Stderr))
}
