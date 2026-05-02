package log

import "testing"

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"trace":   LevelTrace,
		"DEBUG":   LevelDebug,
		"":        LevelInfo,
		"warn":    LevelWarn,
		"warning": LevelWarn,
		"Error":   LevelError,
		"Fatal":   LevelFatal,
		"panic":   LevelFatal,
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil {
			t.Fatalf("%q: unexpected err %v", in, err)
		}
		if got != want {
			t.Fatalf("%q: want %v got %v", in, want, got)
		}
	}
	if _, err := ParseLevel("bogus"); err == nil {
		t.Fatal("expected error for bogus level")
	}
}

func TestLevelCLEFAndShort(t *testing.T) {
	if LevelInfo.CLEFName() != "Information" {
		t.Fatal("CLEFName mismatch")
	}
	if LevelWarn.String() != "WARN" {
		t.Fatal("String mismatch")
	}
}

func TestFromCLEFName(t *testing.T) {
	if FromCLEFName("Warning") != LevelWarn {
		t.Fatal("FromCLEFName failed")
	}
	if FromCLEFName("unknown") != LevelInfo {
		t.Fatal("default mismatch")
	}
}
