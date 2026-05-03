package config

import "encoding/json"

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
//
// 当前未实现 —— Task 19–22 分步补全。
func Preprocess(in PreprocessInput) (*PreprocessResult, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(in.Raw, &raw); err != nil {
		return nil, &PreprocessError{Stage: "parse", Err: err}
	}
	_ = raw // 后续 Task 在此基础上扩展
	return &PreprocessResult{}, nil
}
