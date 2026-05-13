package log

import (
	"strings"
	"testing"
)

func TestParseProfile(t *testing.T) {
	cases := []struct {
		in       string
		want     Profile
		wantAuto bool
		wantErr  bool
	}{
		{"", ProfileNone, true, false},
		{"auto", ProfileNone, true, false},
		{"AUTO", ProfileNone, true, false},
		{"none", ProfileNone, false, false},
		{"never", ProfileNone, false, false},
		{"8", Profile8, false, false},
		{"256", Profile256, false, false},
		{"truecolor", ProfileTrueColor, false, false},
		{"24bit", ProfileTrueColor, false, false},
		{"bogus", ProfileNone, false, true},
	}
	for _, c := range cases {
		got, auto, err := ParseProfile(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseProfile(%q) want err", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseProfile(%q) err = %v", c.in, err)
		}
		if got != c.want || auto != c.wantAuto {
			t.Errorf("ParseProfile(%q) = (%v,%v) want (%v,%v)", c.in, got, auto, c.want, c.wantAuto)
		}
	}
}

func TestLevelColorPrefix(t *testing.T) {
	if LevelColorPrefix(ProfileNone, LevelError) != "" {
		t.Errorf("ProfileNone should be empty")
	}
	if !strings.HasPrefix(LevelColorPrefix(Profile8, LevelError), "\x1b[") {
		t.Errorf("8-color should start with ESC")
	}
	if !strings.Contains(LevelColorPrefix(Profile256, LevelInfo), "38;5;") {
		t.Errorf("256-color should contain 38;5;")
	}
	if !strings.Contains(LevelColorPrefix(ProfileTrueColor, LevelInfo), "38;2;") {
		t.Errorf("truecolor should contain 38;2;")
	}
}

func TestConnPaletteSizes(t *testing.T) {
	if len(ConnPalette(ProfileNone)) != 0 {
		t.Errorf("ProfileNone palette must be empty")
	}
	if len(ConnPalette(Profile8)) != 6 {
		t.Errorf("8-color conn palette want 6, got %d", len(ConnPalette(Profile8)))
	}
	if len(ConnPalette(Profile256)) != 16 {
		t.Errorf("256-color conn palette want 16, got %d", len(ConnPalette(Profile256)))
	}
	if len(ConnPalette(ProfileTrueColor)) != 24 {
		t.Errorf("truecolor conn palette want 24, got %d", len(ConnPalette(ProfileTrueColor)))
	}
}

func TestColorReset(t *testing.T) {
	if ColorReset(ProfileNone) != "" {
		t.Errorf("ProfileNone reset must be empty")
	}
	if ColorReset(Profile8) != "\x1b[0m" {
		t.Errorf("non-none reset must be \\x1b[0m")
	}
}
