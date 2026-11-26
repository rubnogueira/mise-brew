package version

import (
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2", "1.10", -1},
		{"1.2_1", "1.2", 1},
		{"1.2", "1.2_1", -1},
		{"2.0rc1", "2.0", -1},
		{"20261022", "20260901", 1},
		{"3.6.4_1", "3.6.4_1", 0},
		{"1.0.0", "1.0", 0}, // Homebrew pads with zeros
		{"1.0beta2", "1.0rc1", -1},
		{"1.0p1", "1.0", 1},
		{"1.0", "1.0post1", -1},
		{"2.73", "2.8", 1},
		{"1.4.0_3", "1.4.0", 1},
		{"HEAD-abc123", "9.9", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}
