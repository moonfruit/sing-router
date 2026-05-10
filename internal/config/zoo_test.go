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

// ---- rule_set 透传：M5.2 起 dedup 已删除，rule_set 不再被 Preprocess 改写 ----

func TestPreprocessPassesThroughRuleSet(t *testing.T) {
	in := PreprocessInput{
		Raw: zoo(t, map[string]any{
			"outbounds": []any{},
			"route": map[string]any{
				"rule_set": []any{
					map[string]any{"tag": "geosites-cn", "url": "https://x/geosites-cn.srs"},
					map[string]any{"tag": "lan", "url": "https://x/lan.srs"},
				},
				"rules": []any{
					map[string]any{"rule_set": "geosites-cn", "outbound": "DIRECT"},
				},
			},
		}),
	}
	res, err := Preprocess(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.RuleSetCount != 2 {
		t.Fatalf("expected 2 rule_set entries kept verbatim, got %d", res.Stats.RuleSetCount)
	}
	rendered := mustParse(t, res.Rendered)
	rs := rendered["route"].(map[string]any)["rule_set"].([]any)
	if len(rs) != 2 {
		t.Fatalf("rule_set length: got %d want 2", len(rs))
	}
	rules := rendered["route"].(map[string]any)["rules"].([]any)
	if rules[0].(map[string]any)["rule_set"] != "geosites-cn" {
		t.Fatalf("route.rules[0].rule_set should not be rewritten: %#v", rules[0])
	}
}

// ---- outbound collision ----

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

// ---- 集成：综合场景（白名单过滤 + 撞名拒绝 + rule_set 透传） ----

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
		BuiltinOutboundTags: []string{"DIRECT", "REJECT"},
	}
	res, err := Preprocess(in)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Stats.DroppedFields, "log") {
		t.Fatal("log should be marked dropped")
	}
	if res.Stats.OutboundCount != 2 || res.Stats.RuleSetCount != 2 {
		t.Fatalf("stats wrong: %+v", res.Stats)
	}
	rendered := mustParse(t, res.Rendered)
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

// ---- 错误分支：覆盖各 stage 的解析失败路径 ----

func TestPreprocessErrorStages(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		stage string
	}{
		{name: "parse_route", raw: `{"route":"oops"}`, stage: "parse_route"},
		{name: "parse_outbounds", raw: `{"outbounds":"oops"}`, stage: "parse_outbounds"},
		{name: "parse_rule_set", raw: `{"route":{"rule_set":"oops"}}`, stage: "parse_rule_set"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Preprocess(PreprocessInput{Raw: []byte(c.raw)})
			if err == nil {
				t.Fatal("expected error")
			}
			var pe *PreprocessError
			if !errors.As(err, &pe) {
				t.Fatalf("err type %T", err)
			}
			if pe.Stage != c.stage {
				t.Fatalf("stage: got %q want %q", pe.Stage, c.stage)
			}
		})
	}
}

// rules 不是数组 → renderZoo 解 rules 时失败 → 包装为 render
func TestPreprocessRenderError(t *testing.T) {
	in := PreprocessInput{
		Raw: zoo(t, map[string]any{
			"route": map[string]any{
				"rules": "not-an-array",
			},
		}),
	}
	_, err := Preprocess(in)
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *PreprocessError
	if !errors.As(err, &pe) || pe.Stage != "render" {
		t.Fatalf("stage: %v", err)
	}
}

// 直接覆盖 PreprocessError 的 Error() / Unwrap()
func TestPreprocessErrorMethods(t *testing.T) {
	inner := errors.New("boom")
	pe := &PreprocessError{Stage: "x", Err: inner}
	if pe.Error() != "x: boom" {
		t.Fatalf("Error: %q", pe.Error())
	}
	if !errors.Is(pe, inner) {
		t.Fatal("Unwrap should chain to inner")
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
