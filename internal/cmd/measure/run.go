package main

import (
	"fmt"
	"io"
	"os"
)

// run does what the one word of args names in the module at dir, writes what it has to say to
// out, and gives the exit code: 0 done, 1 refused, 2 not run.
func run(args []string, dir string, out io.Writer) int {
	said, code := do(args, dir)
	if _, err := io.WriteString(out, said); err != nil {
		return 2
	}
	return code
}

// do does what run does, and gives what it has to say.
func do(args []string, dir string) (string, int) {
	if len(args) != 1 {
		return "measure takes one word: size\n", 2
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err.Error() + "\n", 2
	}
	said, code := word(root, args[0])
	if err := root.Close(); err != nil && code == 0 {
		return said + err.Error() + "\n", 2
	}
	return said, code
}

// word does what the word w names in root.
func word(root *os.Root, w string) (string, int) {
	switch w {
	case "size":
		return size(root)
	default:
		return fmt.Sprintf("measure takes size, not %q\n", w), 2
	}
}

// size says the module's size and refuses it above Ceiling.
func size(root *os.Root) (string, int) {
	n, err := Size(root.FS())
	if err != nil {
		return err.Error() + "\n", 2
	}
	said := fmt.Sprintf("non-test Go lines outside internal/: %d (the ceiling is %d)\n", n, Ceiling)
	if n > Ceiling {
		return said + fmt.Sprintf("%d lines is %d above the ceiling\n", n, n-Ceiling), 1
	}
	return said, 0
}
