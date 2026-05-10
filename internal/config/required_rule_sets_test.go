package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeRawURL(ref, path string) string {
	return "https://example.com/raw/" + ref + "/" + path + "?access_token=tok"
}

func TestEnsureRequiredRuleSets_WritesMissingTags(t *testing.T) {
	rd := t.TempDir()
	cfgDir := filepath.Join(rd, "config.d")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 静态 fragment 不声明任何 rule_set；user zoo 也未提供
	if err := os.WriteFile(filepath.Join(cfgDir, "dns.json"), []byte(`{"route":{"rules":[{"rule_set":"GeoIP@CN"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	required := []RuleSetSource{{Tag: "GeoIP@CN", GiteePath: "rules/geoip-cn.srs"}}
	added, err := EnsureRequiredRuleSets(rd, "config.d", fakeRawURL, "main", required)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "GeoIP@CN" {
		t.Errorf("added = %v, want [GeoIP@CN]", added)
	}
	data, err := os.ReadFile(filepath.Join(cfgDir, SupplementalRuleSetFile))
	if err != nil {
		t.Fatalf("rule-set.json missing: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("rule-set.json not parseable: %v\n%s", err, data)
	}
	if !strings.Contains(string(data), "https://example.com/raw/main/rules/geoip-cn.srs") {
		t.Errorf("rule-set.json missing expected URL:\n%s", data)
	}
	if !strings.Contains(string(data), "access_token=tok") {
		t.Errorf("rule-set.json missing token query:\n%s", data)
	}
}

func TestEnsureRequiredRuleSets_SkipsWhenProvidedByZoo(t *testing.T) {
	rd := t.TempDir()
	cfgDir := filepath.Join(rd, "config.d")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// zoo.json 已经声明了 GeoIP@CN（模拟 PreprocessZooFile 的产出）
	if err := os.WriteFile(filepath.Join(cfgDir, "zoo.json"),
		[]byte(`{"route":{"rule_set":[{"type":"remote","tag":"GeoIP@CN","url":"https://other/url"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	required := []RuleSetSource{{Tag: "GeoIP@CN", GiteePath: "rules/geoip-cn.srs"}}
	added, err := EnsureRequiredRuleSets(rd, "config.d", fakeRawURL, "main", required)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Errorf("added = %v, want empty (zoo provides it)", added)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, SupplementalRuleSetFile)); !os.IsNotExist(err) {
		t.Errorf("rule-set.json should not exist when nothing missing; err=%v", err)
	}
}

func TestEnsureRequiredRuleSets_RemovesStaleFragmentWhenAllProvided(t *testing.T) {
	rd := t.TempDir()
	cfgDir := filepath.Join(rd, "config.d")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 残留的 rule-set.json（前一次运行写下的）
	if err := os.WriteFile(filepath.Join(cfgDir, SupplementalRuleSetFile),
		[]byte(`{"route":{"rule_set":[{"type":"remote","tag":"Stale","url":"x"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 静态 fragment 已经覆盖了所有 required
	if err := os.WriteFile(filepath.Join(cfgDir, "dns.json"),
		[]byte(`{"route":{"rule_set":[{"type":"remote","tag":"Lan","url":"x"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	required := []RuleSetSource{{Tag: "Lan", GiteePath: "rules/lan.srs"}}
	if _, err := EnsureRequiredRuleSets(rd, "config.d", fakeRawURL, "main", required); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, SupplementalRuleSetFile)); !os.IsNotExist(err) {
		t.Errorf("stale rule-set.json should have been removed; err=%v", err)
	}
}

func TestEnsureRequiredRuleSets_NilRawURLIsNoOp(t *testing.T) {
	rd := t.TempDir()
	cfgDir := filepath.Join(rd, "config.d")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	added, err := EnsureRequiredRuleSets(rd, "config.d", nil, "main", DefaultRequiredRuleSets)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Errorf("added = %v, want empty (rawURL=nil)", added)
	}
}
