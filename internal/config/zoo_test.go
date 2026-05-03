package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// 帮助函数：建一个 zoo 输入字节
func zoo(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustParse(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse rendered: %v", err)
	}
	return m
}

// ---- Task 19: filter ----

func TestPreprocessKeepsOnlyWhitelistedKeys(t *testing.T) {
	in := PreprocessInput{
		Raw: zoo(t, map[string]any{
			"log":       map[string]any{"level": "trace"},
			"dns":       map[string]any{"servers": []any{}},
			"outbounds": []any{map[string]any{"type": "direct", "tag": "via-vps"}},
			"route": map[string]any{
				"rules":                 []any{},
				"rule_set":              []any{},
				"final":                 "via-vps",
				"auto_detect_interface": true, // 不在白名单
			},
			"experimental": map[string]any{"clash_api": map[string]any{}},
		}),
	}
	res, err := Preprocess(in)
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
	rendered := mustParse(t, res.Rendered)
	if _, ok := rendered["log"]; ok {
		t.Fatal("log should be dropped")
	}
	if _, ok := rendered["dns"]; ok {
		t.Fatal("dns should be dropped")
	}
	if _, ok := rendered["experimental"]; ok {
		t.Fatal("experimental should be dropped")
	}
	route, _ := rendered["route"].(map[string]any)
	if _, ok := route["auto_detect_interface"]; ok {
		t.Fatal("route.auto_detect_interface should be dropped")
	}
	if route["final"] != "via-vps" {
		t.Fatal("route.final preserved")
	}
	expect := []string{"dns", "log", "experimental", "route.auto_detect_interface"}
	for _, want := range expect {
		if !contains(res.Stats.DroppedFields, want) {
			t.Errorf("expected dropped field %q in stats", want)
		}
	}
	if res.Stats.OutboundCount != 1 {
		t.Fatal("OutboundCount mismatch")
	}
}

// ---- Task 20: dedup by URL ----

func TestPreprocessDedupRuleSetByURL(t *testing.T) {
	in := PreprocessInput{
		Raw: zoo(t, map[string]any{
			"outbounds": []any{},
			"route": map[string]any{
				"rule_set": []any{
					map[string]any{"tag": "geosites-cn", "url": "https://x/geosites-cn.srs"},
					map[string]any{"tag": "lan", "url": "https://x/lan.srs"},
					map[string]any{"tag": "extra", "url": "https://x/extra.srs"},
				},
				"rules": []any{},
			},
		}),
		BuiltinRuleSetIndex: []RuleSetEntry{
			{Tag: "GeoSites@CN", URL: "https://x/geosites-cn.srs"},
			{Tag: "Lan", URL: "https://x/lan.srs"},
		},
	}
	res, err := Preprocess(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.RuleSetDedupDropped != 2 {
		t.Fatalf("expected 2 dropped, got %d", res.Stats.RuleSetDedupDropped)
	}
	if res.Stats.RuleSetCount != 1 {
		t.Fatalf("expected 1 remaining, got %d", res.Stats.RuleSetCount)
	}
	rendered := mustParse(t, res.Rendered)
	rs := rendered["route"].(map[string]any)["rule_set"].([]any)
	if len(rs) != 1 || rs[0].(map[string]any)["tag"] != "extra" {
		t.Fatalf("unexpected remaining rule_set: %#v", rs)
	}
}

// ---- Task 21: rewrite references ----

func TestPreprocessRewritesRouteRulesRefsToBuiltinTags(t *testing.T) {
	in := PreprocessInput{
		Raw: zoo(t, map[string]any{
			"outbounds": []any{},
			"route": map[string]any{
				"rule_set": []any{
					map[string]any{"tag": "geosites-cn", "url": "https://x/geosites-cn.srs"},
				},
				"rules": []any{
					map[string]any{"rule_set": "geosites-cn", "outbound": "DIRECT"},
					map[string]any{"rule_set": "extra", "outbound": "proxy"},
				},
			},
		}),
		BuiltinRuleSetIndex: []RuleSetEntry{
			{Tag: "GeoSites@CN", URL: "https://x/geosites-cn.srs"},
		},
	}
	res, err := Preprocess(in)
	if err != nil {
		t.Fatal(err)
	}
	rendered := mustParse(t, res.Rendered)
	rules := rendered["route"].(map[string]any)["rules"].([]any)
	if rules[0].(map[string]any)["rule_set"] != "GeoSites@CN" {
		t.Fatalf("rewrite failed: %#v", rules[0])
	}
	if rules[1].(map[string]any)["rule_set"] != "extra" {
		t.Fatalf("non-deduped rule_set should remain unchanged: %#v", rules[1])
	}
}

// ---- Task 22: outbound collision ----

func TestPreprocessRejectsOutboundTagCollision(t *testing.T) {
	in := PreprocessInput{
		Raw: zoo(t, map[string]any{
			"outbounds": []any{
				map[string]any{"type": "direct", "tag": "DIRECT"},
			},
			"route": map[string]any{"rules": []any{}, "rule_set": []any{}, "final": "DIRECT"},
		}),
		BuiltinOutboundTags: []string{"DIRECT", "REJECT"},
	}
	_, err := Preprocess(in)
	if err == nil {
		t.Fatal("expected collision error")
	}
	var pe *PreprocessError
	if !errors.As(err, &pe) {
		t.Fatalf("err type %T", err)
	}
	if pe.Stage != "outbound_collision" {
		t.Fatalf("stage: %s", pe.Stage)
	}
}

// ---- 集成：综合场景 ----

func TestPreprocessIntegrationWalkAll(t *testing.T) {
	in := PreprocessInput{
		Raw: zoo(t, map[string]any{
			"log": map[string]any{"level": "trace"},
			"outbounds": []any{
				map[string]any{"type": "anytls", "tag": "jp"},
				map[string]any{"type": "anytls", "tag": "us"},
			},
			"route": map[string]any{
				"rule_set": []any{
					map[string]any{"tag": "geosites-cn", "url": "https://x/geosites-cn.srs"},
					map[string]any{"tag": "ads", "url": "https://x/ads.srs"},
				},
				"rules": []any{
					map[string]any{"rule_set": "geosites-cn", "outbound": "DIRECT"},
					map[string]any{"rule_set": "ads", "outbound": "REJECT"},
				},
				"final": "jp",
			},
		}),
		BuiltinRuleSetIndex: []RuleSetEntry{
			{Tag: "GeoSites@CN", URL: "https://x/geosites-cn.srs"},
		},
		BuiltinOutboundTags: []string{"DIRECT", "REJECT"},
	}
	res, err := Preprocess(in)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Stats.DroppedFields, "log") {
		t.Fatal("log should be marked dropped")
	}
	if res.Stats.OutboundCount != 2 || res.Stats.RuleSetCount != 1 || res.Stats.RuleSetDedupDropped != 1 {
		t.Fatalf("stats wrong: %+v", res.Stats)
	}
	rendered := mustParse(t, res.Rendered)
	rules := rendered["route"].(map[string]any)["rules"].([]any)
	if rules[0].(map[string]any)["rule_set"] != "GeoSites@CN" {
		t.Fatal("rewrite failed in integration")
	}
	if rendered["route"].(map[string]any)["final"] != "jp" {
		t.Fatal("final preserved")
	}
}

func TestPreprocessParseError(t *testing.T) {
	_, err := Preprocess(PreprocessInput{Raw: []byte("not json")})
	if err == nil {
		t.Fatal("expected parse error")
	}
	var pe *PreprocessError
	if !errors.As(err, &pe) {
		t.Fatalf("err type %T", err)
	}
	if pe.Stage != "parse" {
		t.Fatalf("stage: %s", pe.Stage)
	}
}

func TestPreprocessJSONOutputRetainsKeyOrder(t *testing.T) {
	// outbounds 应在 route 之前；rule_set 在 rules 之前；rules 在 final 之前。
	in := PreprocessInput{
		Raw: zoo(t, map[string]any{
			"route": map[string]any{
				"final":    "DIRECT",
				"rules":    []any{},
				"rule_set": []any{},
			},
			"outbounds": []any{},
		}),
	}
	res, err := Preprocess(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(res.Rendered)
	if !(strings.Index(s, `"outbounds"`) < strings.Index(s, `"route"`)) {
		t.Fatalf("expected outbounds before route in rendered JSON: %s", s)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
