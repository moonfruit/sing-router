package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RuleSetSource 描述一个静态 fragment 引用、但没有定义的 rule_set。
// EnsureRequiredRuleSets 在用户 zoo.raw.json 也没声明该 tag 时按这个表生成
// 一个补充 fragment。
type RuleSetSource struct {
	Tag       string // sing-box rule_set tag（如 "GeoIP@CN"）
	GiteePath string // gitee 仓库内路径（如 "rules/geoip-cn.srs"）
}

// DefaultRequiredRuleSets 是当前嵌入的静态 fragment（dns.json 等）所引用、
// 但不再自行声明的 rule_set 集合。仓库内路径基于约定：rules/{slug}.srs。
var DefaultRequiredRuleSets = []RuleSetSource{
	{Tag: "GeoIP@CN", GiteePath: "rules/geoip-cn.srs"},
	{Tag: "GeoSites@CN", GiteePath: "rules/geosites-cn.srs"},
	{Tag: "Lan", GiteePath: "rules/lan.srs"},
	{Tag: "FakeIpBypass", GiteePath: "rules/fakeip-bypass.srs"},
}

// SupplementalRuleSetFile 是 EnsureRequiredRuleSets 自动生成的 fragment 文件名。
// 这个名字稳定，便于排障 / 反复重写。
const SupplementalRuleSetFile = "rule-set.json"

// RawURLFunc 用闭包注入 URL 构造逻辑，避免 internal/config 反向依赖 internal/gitee。
// 实现一般是 (*gitee.Client).RawURL；带 ?access_token 的真实 gitee API URL。
type RawURLFunc func(ref, path string) string

// EnsureRequiredRuleSets 检查 configDir 下的 fragment（含 PreprocessZooFile
// 写好的 zoo.json）已经声明了哪些 rule_set tag，凡是 required 中缺失的，写入
// <configDir>/rule-set.json。每条 entry 的 url 由 rawURL(ref, path) 生成——
// 真实 gitee URL，无需走本地反向代理（不需要 sing-box 启动早于 daemon proxy）。
//
//   - rawURL == nil 或 required 为空：删除可能残留的 rule-set.json，返回 nil。
//   - 全部 required 已被覆盖：删除残留 rule-set.json，返回 nil。
//   - 否则：原子写 rule-set.json，返回补充进去的 tag 列表。
func EnsureRequiredRuleSets(rundir, configDir string, rawURL RawURLFunc, ref string, required []RuleSetSource) ([]string, error) {
	cfgDir := filepath.Join(rundir, configDir)
	rsPath := filepath.Join(cfgDir, SupplementalRuleSetFile)

	if rawURL == nil || len(required) == 0 {
		_ = os.Remove(rsPath)
		return nil, nil
	}
	provided, err := scanProvidedRuleSetTags(cfgDir)
	if err != nil {
		return nil, err
	}
	var missing []RuleSetSource
	for _, r := range required {
		if !provided[r.Tag] {
			missing = append(missing, r)
		}
	}
	if len(missing) == 0 {
		_ = os.Remove(rsPath)
		return nil, nil
	}
	var supplemented []string
	var entries []map[string]any
	for _, r := range missing {
		entries = append(entries, map[string]any{
			"type": "remote",
			"tag":  r.Tag,
			"url":  rawURL(ref, r.GiteePath),
		})
		supplemented = append(supplemented, r.Tag)
	}
	out := map[string]any{
		"route": map[string]any{
			"rule_set": entries,
		},
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return nil, err
	}
	tmp := rsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, rsPath); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("rename %s -> %s: %w", tmp, rsPath, err)
	}
	return supplemented, nil
}

// scanProvidedRuleSetTags 收集 configDir 下所有 *.json fragment（除 rule-set.json
// 自身）已声明的 rule_set tag。无法解析的 fragment 跳过，不影响正确性。
func scanProvidedRuleSetTags(configDir string) (map[string]bool, error) {
	out := map[string]bool{}
	entries, err := os.ReadDir(configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if e.Name() == SupplementalRuleSetFile {
			continue
		}
		data, err := os.ReadFile(filepath.Join(configDir, e.Name()))
		if err != nil {
			return nil, err
		}
		var doc struct {
			Route struct {
				RuleSet []struct {
					Tag string `json:"tag"`
				} `json:"rule_set"`
			} `json:"route"`
		}
		if err := json.Unmarshal(stripJSONCLineComments(data), &doc); err != nil {
			continue
		}
		for _, rs := range doc.Route.RuleSet {
			if rs.Tag != "" {
				out[rs.Tag] = true
			}
		}
	}
	return out, nil
}
