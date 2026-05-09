package version

import "testing"

func TestStringDefaults(t *testing.T) {
	Version = ""
	if got := String(); got != "dev" {
		t.Fatalf("want dev, got %q", got)
	}
}

func TestStringRespectsLdflag(t *testing.T) {
	Version = "0.1.0+abcdef"
	if got := String(); got != "0.1.0+abcdef" {
		t.Fatalf("want injected version, got %q", got)
	}
}
