package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const seedZooJSON = `{"outbounds":[{"type":"selector","tag":"主","outbounds":["DIRECT"]}]}`

const userZooRawJSON = `{
  "outbounds": [
    {"type": "direct", "tag": "DIRECT-USER"},
    {"type": "selector", "tag": "Proxy", "outbounds": ["DIRECT-USER"]}
  ],
  "route": {
    "rule_set": [
      {"type": "remote", "tag": "UserRules", "url": "http://x/user.srs"}
    ],
    "rules": [
      {"rule_set": "UserRules", "outbound": "Proxy"}
    ]
  }
}`

const builtinOutboundsJSON = `// header
{
  "outbounds": [
    {"type": "direct", "tag": "DIRECT"},
    {"type": "block",  "tag": "REJECT"}
  ]
}`

const builtinDNSJSON = `{
  "route": {
    "rule_set": [
      {"type": "remote", "tag": "GeoIP@CN", "url": "http://x/geoip.srs"}
    ]
  }
}`

func writeRundir(t *testing.T) string {
	t.Helper()
	rd := t.TempDir()
	for _, d := range []string{"var", "config.d"} {
		if err := os.MkdirAll(filepath.Join(rd, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rd, "config.d", "zoo.json"), []byte(seedZooJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rd, "config.d", "outbounds.json"), []byte(builtinOutboundsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rd, "config.d", "dns.json"), []byte(builtinDNSJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return rd
}

func TestPreprocessZooFile_ReplacesSeedWithUserContent(t *testing.T) {
	rd := writeRundir(t)
	if err := os.WriteFile(filepath.Join(rd, "var", "zoo.raw.json"), []byte(userZooRawJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err := PreprocessZooFile(rd, "config.d")
	if err != nil {
		t.Fatalf("PreprocessZooFile: %v", err)
	}
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}
	if stats.OutboundCount != 2 {
		t.Errorf("OutboundCount = %d, want 2", stats.OutboundCount)
	}
	data, err := os.ReadFile(filepath.Join(rd, "config.d", "zoo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == seedZooJSON {
		t.Fatal("config.d/zoo.json was not replaced")
	}
	if !strings.Contains(string(data), "DIRECT-USER") {
		t.Errorf("processed zoo.json missing user outbound DIRECT-USER:\n%s", data)
	}
}

func TestPreprocessZooFile_NoRawFile_NoOp(t *testing.T) {
	rd := writeRundir(t)
	stats, err := PreprocessZooFile(rd, "config.d")
	if err != nil {
		t.Fatalf("PreprocessZooFile: %v", err)
	}
	if stats != nil {
		t.Errorf("expected nil stats when raw missing, got %+v", stats)
	}
	data, err := os.ReadFile(filepath.Join(rd, "config.d", "zoo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != seedZooJSON {
		t.Errorf("seed zoo.json should be untouched; got: %s", data)
	}
}

func TestPreprocessZooFile_BuiltinOutboundCollisionRejected(t *testing.T) {
	rd := writeRundir(t)
	// 用户 outbounds 含有与静态 fragment 撞名的 tag "DIRECT"
	colliding := `{"outbounds":[{"type":"direct","tag":"DIRECT"}]}`
	if err := os.WriteFile(filepath.Join(rd, "var", "zoo.raw.json"), []byte(colliding), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := PreprocessZooFile(rd, "config.d")
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
}

func TestScanZooBuiltins_PicksUpStaticFragments(t *testing.T) {
	rd := writeRundir(t)
	tags, rules, err := scanZooBuiltins(filepath.Join(rd, "config.d"))
	if err != nil {
		t.Fatal(err)
	}
	wantTags := map[string]bool{"DIRECT": true, "REJECT": true}
	got := map[string]bool{}
	for _, t := range tags {
		got[t] = true
	}
	for k := range wantTags {
		if !got[k] {
			t.Errorf("missing builtin tag %q in scanned %v", k, tags)
		}
	}
	if len(rules) != 1 || rules[0].Tag != "GeoIP@CN" {
		t.Errorf("rules = %+v; want one entry with tag GeoIP@CN", rules)
	}
}

func TestPreprocessZooFile_TagCollisionDroppedByBuiltinWins(t *testing.T) {
	rd := writeRundir(t)
	// 用户 zoo 含与静态 dns.json 同名的 GeoIP@CN，但 URL 不同
	colliding := `{
      "outbounds": [{"type": "direct", "tag": "Custom"}],
      "route": {
        "rule_set": [
          {"type": "remote", "tag": "GeoIP@CN", "url": "http://other.host/geoip.srs"},
          {"type": "remote", "tag": "MyOwn",    "url": "http://x/mine.srs"}
        ],
        "rules": [{"rule_set": "GeoIP@CN", "outbound": "Custom"}]
      }
    }`
	if err := os.WriteFile(filepath.Join(rd, "var", "zoo.raw.json"), []byte(colliding), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err := PreprocessZooFile(rd, "config.d")
	if err != nil {
		t.Fatalf("PreprocessZooFile: %v", err)
	}
	if stats.RuleSetDedupDropped != 1 {
		t.Errorf("RuleSetDedupDropped = %d, want 1 (GeoIP@CN dropped)", stats.RuleSetDedupDropped)
	}
	data, err := os.ReadFile(filepath.Join(rd, "config.d", "zoo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "other.host") {
		t.Errorf("user's GeoIP@CN URL should be dropped (builtin wins); got:\n%s", data)
	}
	if !strings.Contains(string(data), "MyOwn") {
		t.Errorf("non-colliding rule-set MyOwn should be kept; got:\n%s", data)
	}
}

func TestStripJSONCLineComments_PreservesURLs(t *testing.T) {
	src := []byte(`{
  // header
  "url": "http://127.0.0.1/x"
}`)
	clean := stripJSONCLineComments(src)
	var doc map[string]string
	if err := json.Unmarshal(clean, &doc); err != nil {
		t.Fatalf("strip+unmarshal failed: %v\n--- clean ---\n%s", err, clean)
	}
	if doc["url"] != "http://127.0.0.1/x" {
		t.Errorf("URL clobbered: %q", doc["url"])
	}
}
