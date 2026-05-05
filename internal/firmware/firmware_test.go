package firmware

import (
	"errors"
	"fmt"
	"testing"
)

func TestKindString(t *testing.T) {
	cases := []struct {
		k    Kind
		want string
	}{
		{KindKoolshare, "koolshare"},
		{KindMerlin, "merlin"},
	}
	for _, c := range cases {
		if string(c.k) != c.want {
			t.Errorf("Kind=%q want %q", c.k, c.want)
		}
	}
}

func TestErrUnknownIsSentinel(t *testing.T) {
	wrapped := fmt.Errorf("detect failed: %w", ErrUnknown)
	if !errors.Is(wrapped, ErrUnknown) {
		t.Fatal("errors.Is(wrapped, ErrUnknown) should match through %w wrapping")
	}
}
