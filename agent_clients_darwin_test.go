package gosocket_test

import "testing"

// agentClients are the OpenSSH clients a program meets on macOS: the one
// macOS ships.
func agentClients(t *testing.T) []agentClient {
	t.Helper()
	return []agentClient{{name: "macOS", dir: "/usr/bin", lists: true}}
}
