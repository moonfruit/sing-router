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
// 一个补充 fragment——优先用真实 gitee URL；token 缺失时退回 install 阶段
// 落到 <rundir>/var/rules/ 的内嵌 srs 文件（local 类型）。
type RuleSetSource struct {
	Tag          string // sing-box rule_set tag（如 "GeoIP@CN"）
	GiteePath    string // gitee 仓库内路径（如 "rules/geoip-cn.srs"）
	LocalRelPath string // 相对 rundir 的本地文件路径（如 "var/rules/geoip-cn.srs"）
}

// DefaultRequiredRuleSets 是当前嵌入的静态 fragment（dns.json 等）所引用、
// 但不再自行声明的 rule_set 集合。
var DefaultRequiredRuleSets = []RuleSetSource{
	{Tag: "GeoIP@CN", GiteePath: "rules/geoip-cn.srs", LocalRelPath: "var/rules/geoip-cn.srs"},
	{Tag: "GeoSites@CN", GiteePath: "rules/geosites-cn.srs", LocalRelPath: "var/rules/geosites-cn.srs"},
	{Tag: "Lan", GiteePath: "rules/lan.srs", LocalRelPath: "var/rules/lan.srs"},
	{Tag: "FakeIpBypass", GiteePath: "rules/fakeip-bypass.srs", LocalRelPath: "var/rules/fakeip-bypass.srs"},
}

// SupplementalRuleSetFile 是 EnsureRequiredRuleSets 自动生成的 fragment 文件名。
const SupplementalRuleSetFile = "rule-set.json"

// RawURLFunc 用闭包注入 URL 构造逻辑，避免 internal/config 反向依赖 internal/gitee。
// 实现一般是 (*gitee.Client).RawURL；带 ?access_token 的真实 gitee API URL。
type RawURLFunc func(ref, path string) string

// EnsureRequiredRuleSets 检查 configDir 下的 fragment（含 PreprocessZooFile
// 写好的 zoo.json）已经声明了哪些 rule_set tag，凡是 required 中缺失的，写入
// <configDir>/rule-set.json。
//
// 选择策略（per missing tag）：
//   - rawURL != nil && ref != ""：写 remote 类型，URL 来自 rawURL(ref, GiteePath)
//   - 否则：写 local 类型，path = LocalRelPath（相对 rundir，sing-box 在
//     run -D <rundir> 下解析）。前提是 install 阶段已把 srs 文件落到 var/rules/。
//
// 全部 required 都已覆盖：删除残留 rule-set.json，返回 nil。
// required 为空：同上，删 + 返 nil。
func EnsureRequiredRuleSets(rundir, configDir string, rawURL RawURLFunc, ref string, required []RuleSetSource) ([]string, error) {
	cfgDir := filepath.Join(rundir, configDir)
	rsPath := filepath.Join(cfgDir, SupplementalRuleSetFile)

	if len(required) == 0 {
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
	useRemote := rawURL != nil && ref != ""

	var supplemented []string
	var entries []map[string]any
	for _, r := range missing {
		entry := map[string]any{"tag": r.Tag}
		if useRemote {
			entry["type"] = "remote"
			entry["url"] = rawURL(ref, r.GiteePath)
		} else {
			entry["type"] = "local"
			entry["path"] = r.LocalRelPath
		}
		entries = append(entries, entry)
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
