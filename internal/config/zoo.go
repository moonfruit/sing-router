package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	keyOutbounds    = "outbounds"
	keyRoute        = "route"
	keyRouteRules   = "rules"
	keyRouteRuleSet = "rule_set"
	keyRouteFinal   = "final"
)

// 白名单（顶层 / route 子键）
var (
	topLevelWhitelist = map[string]struct{}{
		keyOutbounds: {}, keyRoute: {},
	}
	routeWhitelist = map[string]struct{}{
		keyRouteRules: {}, keyRouteRuleSet: {}, keyRouteFinal: {},
	}
)

// PreprocessStats 是单次 zoo 预处理的统计结果，进入 status API。
type PreprocessStats struct {
	OutboundCount             int      `json:"outbound_count"`
	RuleSetCount              int      `json:"rule_set_count"`
	RuleSetDedupDropped       int      `json:"rule_set_dedup_dropped"`
	OutboundCollisionRejected bool     `json:"outbound_collision_rejected"`
	DroppedFields             []string `json:"dropped_fields"`
}

// PreprocessInput 描述一次预处理的输入。
type PreprocessInput struct {
	Raw                 []byte
	BuiltinRuleSetIndex []RuleSetEntry // 来自所有静态 fragment 的 route.rule_set
	BuiltinOutboundTags []string       // 静态 outbounds.json 的 tag 列表（如 DIRECT、REJECT）
}

// RuleSetEntry 描述 rule_set 的最少字段（tag + url）；按 url 去重。
type RuleSetEntry struct {
	Tag string `json:"tag"`
	URL string `json:"url,omitempty"`
}

// PreprocessResult 是一次成功预处理的产出。
type PreprocessResult struct {
	Rendered []byte
	Stats    PreprocessStats
}

// PreprocessError 表示预处理本身或其结果不接受（应回滚到 last-good）。
type PreprocessError struct {
	Stage string
	Err   error
}

func (e *PreprocessError) Error() string {
	return e.Stage + ": " + e.Err.Error()
}

func (e *PreprocessError) Unwrap() error { return e.Err }

// Preprocess 对 zoo.raw.json 的字节做白名单过滤、URL 去重、引用改写、撞名校验，
// 返回最终可写入 config.d/zoo.json 的字节。
func Preprocess(in PreprocessInput) (*PreprocessResult, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(in.Raw, &raw); err != nil {
		return nil, &PreprocessError{Stage: "parse", Err: err}
	}

	var dropped []string
	for k := range raw {
		if _, ok := topLevelWhitelist[k]; !ok {
			dropped = append(dropped, k)
			delete(raw, k)
		}
	}

	// 处理 route 子键
	var route map[string]json.RawMessage
	if raw[keyRoute] != nil {
		if err := json.Unmarshal(raw[keyRoute], &route); err != nil {
			return nil, &PreprocessError{Stage: "parse_route", Err: err}
		}
		for k := range route {
			if _, ok := routeWhitelist[k]; !ok {
				dropped = append(dropped, "route."+k)
				delete(route, k)
			}
		}
	}
	sort.Strings(dropped)

	// 解析 outbounds 与 rule_set，便于统计与后续步骤
	var outbounds []map[string]any
	if raw[keyOutbounds] != nil {
		if err := json.Unmarshal(raw[keyOutbounds], &outbounds); err != nil {
			return nil, &PreprocessError{Stage: "parse_outbounds", Err: err}
		}
	}

	var ruleSetEntries []map[string]any
	if route != nil && route[keyRouteRuleSet] != nil {
		if err := json.Unmarshal(route[keyRouteRuleSet], &ruleSetEntries); err != nil {
			return nil, &PreprocessError{Stage: "parse_rule_set", Err: err}
		}
	}

	stats := PreprocessStats{
		OutboundCount: len(outbounds),
		RuleSetCount:  len(ruleSetEntries),
		DroppedFields: dropped,
	}

	// Task 20/21/22 在此基础上插入：
	// 1. dedup ruleSetEntries by URL → 改写 stats.RuleSetCount / Dropped
	// 2. rewrite route.rules tag refs
	// 3. outbound collision check

	rendered, err := renderZoo(outbounds, ruleSetEntries, route)
	if err != nil {
		return nil, &PreprocessError{Stage: "render", Err: err}
	}
	return &PreprocessResult{Rendered: rendered, Stats: stats}, nil
}

// renderZoo 输出顺序固定为 outbounds → route.{rule_set, rules, final}
func renderZoo(outbounds []map[string]any, ruleSet []map[string]any, route map[string]json.RawMessage) ([]byte, error) {
	out := newOrderedJSON()
	if outbounds != nil {
		out.Set(keyOutbounds, outbounds)
	}

	routeOut := newOrderedJSON()
	if ruleSet != nil {
		routeOut.Set(keyRouteRuleSet, ruleSet)
	}
	if route != nil && route[keyRouteRules] != nil {
		var rules []map[string]any
		if err := json.Unmarshal(route[keyRouteRules], &rules); err != nil {
			return nil, fmt.Errorf("parse route.rules: %w", err)
		}
		routeOut.Set(keyRouteRules, rules)
	}
	if route != nil && route[keyRouteFinal] != nil {
		var f any
		_ = json.Unmarshal(route[keyRouteFinal], &f)
		routeOut.Set(keyRouteFinal, f)
	}
	if len(routeOut.keys) > 0 {
		out.Set(keyRoute, routeOut)
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// orderedJSON 是局部使用的有序 map，确保输出顺序可预测。
// 与 internal/log/clef.OrderedEvent 类似，但避免跨包循环依赖。
type orderedJSON struct {
	keys   []string
	values map[string]any
}

func newOrderedJSON() *orderedJSON { return &orderedJSON{values: map[string]any{}} }

func (o *orderedJSON) Set(k string, v any) {
	if _, ok := o.values[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.values[k] = v
}

func (o *orderedJSON) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(o.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
