package caller

import "testing"

// A version reads as its major and minor, the long form as its short one;
// one whose major or minor is not decimal digits alone is no version.
//
// What it costs when this fails: a program holds tailscaled to its floor by
// numbers that are not tailscaled's, and a serve that passes a caller's own
// grants header on passes the program's check.
func TestParseVersionReadsDecimalDigitsAlone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		version      string
		major, minor int
		ok           bool
	}{
		{"1.94.0", 1, 94, true},
		{"1.94", 1, 94, true},
		{"1.102.5", 1, 102, true},
		{"1.94.0-t0123abcd-g4567ef", 1, 94, true},
		{"1.92.5-t0123abcd-g4567ef", 1, 92, true},
		{"2.0.0", 2, 0, true},
		{"0.99.0", 0, 99, true},
		{"", 0, 0, false},
		{"1", 0, 0, false},
		{"one.two", 0, 0, false},
		{"1.-2.0", 0, 0, false},
		{"+1.94.0", 0, 0, false},
		{"1.+94.0", 0, 0, false},
		{" 1.94.0", 0, 0, false},
		{"1.94 ", 0, 0, false},
		{"1.٩٤.0", 0, 0, false},
		{"99999999999999999999.1", 0, 0, false},
	}
	for _, c := range cases {
		major, minor, ok := ParseVersion(c.version)
		if major != c.major || minor != c.minor || ok != c.ok {
			t.Errorf("%q: %d.%d %v, want %d.%d %v", c.version, major, minor, ok, c.major, c.minor, c.ok)
		}
	}
	if len(cases) != 17 {
		t.Fatalf("held %d of 17 versions", len(cases))
	}
}
