//go:build !windows

package main

// killedHow is how the end line says a binary ended by os.Process.Kill ended: on a Unix, SIGKILL.
const killedHow = "ended by signal: killed"
