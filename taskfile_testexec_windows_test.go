package gosocket_test

// prePushThroughTestexec is whether pre-push's run of the race tests starts each test binary
// through internal/cmd/testexec on this system. It does on Windows, where two of ten pre-push
// tiers failed in the test member with no test line, at the cost of the cache.
const prePushThroughTestexec = true
