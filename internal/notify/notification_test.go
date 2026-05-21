package notify

import "testing"

func TestParsePriority(t *testing.T) {
	cases := []struct {
		in   string
		want Priority
		ok   bool
	}{
		{"", PriorityLow, true},
		{"low", PriorityLow, true},
		{"normal", PriorityNormal, true},
		{"high", PriorityHigh, true},
		{"critical", PriorityCritical, true},
		{"bogus", PriorityLow, false},
	}
	for _, c := range cases {
		got, ok := ParsePriority(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParsePriority(%q) = (%v,%v), want (%v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestPriorityStringRoundTrip(t *testing.T) {
	for _, p := range []Priority{PriorityLow, PriorityNormal, PriorityHigh, PriorityCritical} {
		got, ok := ParsePriority(p.String())
		if !ok || got != p {
			t.Errorf("round-trip %v -> %q -> (%v,%v)", p, p.String(), got, ok)
		}
	}
}
