//go:build !windows

package gosocket_test

// prePushThroughTestexec is whether pre-push's run of the race tests starts each test binary
// through internal/cmd/testexec on this system. It does not here: go test caches nothing it
// runs through -exec, and pre-push takes the race tests from the cache.
const prePushThroughTestexec = false
