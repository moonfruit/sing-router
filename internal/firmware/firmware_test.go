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

func TestByName(t *testing.T) {
	cases := []struct {
		name    string
		wantKind Kind
		wantErr bool
	}{
		{"koolshare", KindKoolshare, false},
		{"merlin", KindMerlin, false},
		{"Koolshare", "", true},
		{"openwrt", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := ByName(c.name)
		if c.wantErr {
			if err == nil {
				t.Errorf("ByName(%q) want err, got nil", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("ByName(%q) unexpected err: %v", c.name, err)
			continue
		}
		if got.Kind() != c.wantKind {
			t.Errorf("ByName(%q) Kind=%q want %q", c.name, got.Kind(), c.wantKind)
		}
	}
}

func TestNewReturnsCorrectKind(t *testing.T) {
	if New(KindKoolshare).Kind() != KindKoolshare {
		t.Error("New(KindKoolshare) wrong Kind")
	}
	if New(KindMerlin).Kind() != KindMerlin {
		t.Error("New(KindMerlin) wrong Kind")
	}
}
