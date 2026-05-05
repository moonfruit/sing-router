package firmware

import (
	"errors"
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
	wrapped := errors.New("x: " + ErrUnknown.Error())
	_ = wrapped
	if !errors.Is(ErrUnknown, ErrUnknown) {
		t.Fatal("ErrUnknown should match itself via errors.Is")
	}
}
