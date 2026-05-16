package log

import (
	"testing"

	"github.com/moonfruit/sing2seq/clef"
)

func TestLevelAtLeast(t *testing.T) {
	makeEv := func(lvl string) *clef.Event {
		ev := clef.NewEvent()
		if lvl != "" {
			ev.Set("@l", lvl)
		}
		return ev
	}

	cases := []struct {
		name string
		min  Level
		lvl  string
		want bool
	}{
		{"trace floor accepts all", LevelTrace, "Verbose", true},
		{"trace floor accepts no @l", LevelTrace, "", true},
		{"trace floor accepts bogus", LevelTrace, "bogus", true},
		{"warn floor drops info", LevelWarn, "Information", false},
		{"warn floor passes warning", LevelWarn, "Warning", true},
		{"warn floor passes error", LevelWarn, "Error", true},
		{"warn floor passes fatal", LevelWarn, "Fatal", true},
		{"warn floor drops missing @l (defaults info)", LevelWarn, "", false},
		{"warn floor drops bogus @l (defaults info)", LevelWarn, "bogus", false},
		{"error floor drops warning", LevelError, "Warning", false},
		{"error floor passes error", LevelError, "Error", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := LevelAtLeast(c.min)
			if got := f(makeEv(c.lvl)); got != c.want {
				t.Fatalf("min=%v lvl=%q got=%v want=%v", c.min, c.lvl, got, c.want)
			}
		})
	}
}
