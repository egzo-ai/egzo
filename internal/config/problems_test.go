package config

import "testing"

func TestProblemsErr(t *testing.T) {
	var p problems
	if err := p.err(); err != nil {
		t.Fatalf("err() = %v, want nil when nothing was added", err)
	}
	p.addf("first %d", 1)
	p.addf("second %s", "two")
	if got, want := p.err().Error(), "first 1\nsecond two"; got != want {
		t.Errorf("err() = %q, want %q", got, want)
	}
}

func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"kitten", "sitting", 3},
		{"anthropic", "antropic", 1},
		{"ab", "ba", 2},
	}
	for _, c := range cases {
		if got := editDistance(c.a, c.b); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := editDistance(c.b, c.a); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d (symmetry)", c.b, c.a, got, c.want)
		}
	}
}

func TestSuggestPicksTheClosest(t *testing.T) {
	if got := suggest("githab", []string{"anthropic", "github", "gitlab"}); got != "github" {
		t.Errorf("suggest = %q", got)
	}
	if got := suggest("x", nil); got != "" {
		t.Errorf("suggest with no candidates = %q", got)
	}
}
