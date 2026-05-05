package firmware

import "testing"

func TestFakeNvramGet(t *testing.T) {
	f := fakeNvram{"jffs2_scripts": "1", "model": "RT-BE88U"}
	v, err := f.Get("jffs2_scripts")
	if err != nil || v != "1" {
		t.Fatalf("got (%q, %v) want (\"1\", nil)", v, err)
	}
	v, err = f.Get("missing")
	if err != nil {
		t.Fatalf("missing key should not error, got %v", err)
	}
	if v != "" {
		t.Fatalf("missing key should return empty string, got %q", v)
	}
}

func TestShellNvramGetTrimmed(t *testing.T) {
	old := nvramExec
	t.Cleanup(func() { nvramExec = old })
	nvramExec = func(args ...string) ([]byte, error) {
		if len(args) != 2 || args[0] != "get" || args[1] != "extendno" {
			t.Fatalf("unexpected args %v", args)
		}
		return []byte("37094_koolcenter\n"), nil
	}
	got, err := (shellNvram{}).Get("extendno")
	if err != nil || got != "37094_koolcenter" {
		t.Fatalf("got (%q, %v) want (37094_koolcenter, nil)", got, err)
	}
}
