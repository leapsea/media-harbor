package engine

import "testing"

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"2026.10.21", "2026.08.19", 1}, {"2026.08.19", "2026.8.19", 0}, {"1.32.17", "1.32.9", 1}, {"1.32", "1.32.1", -1}, {"", "1.0", -1}} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
