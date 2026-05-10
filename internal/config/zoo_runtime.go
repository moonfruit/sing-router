package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PreprocessZooFile 读取 <rundir>/var/zoo.raw.json，扫描 <rundir>/<configDir>
// 下其它 *.json fragment 取得静态 outbound tag 与 rule_set 列表，运行 Preprocess，
// 原子写入 <rundir>/<configDir>/zoo.json。
//
// 缺失 zoo.raw.json 不视为错误：返回 (nil, nil)，调用方继续用种子 zoo.json。
// 其它错误返回，由调用方决定 log + 继续 还是 fail。
func PreprocessZooFile(rundir, configDir string) (*PreprocessStats, error) {
	rawPath := filepath.Join(rundir, "var", "zoo.raw.json")
	raw, err := os.ReadFile(rawPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rawPath, err)
	}
	cfgDir := filepath.Join(rundir, configDir)
	tags, ruleSets, err := scanZooBuiltins(cfgDir)
	if err != nil {
		return nil, fmt.Errorf("scan fragments: %w", err)
	}
	res, err := Preprocess(PreprocessInput{
		Raw:                 raw,
		BuiltinOutboundTags: tags,
		BuiltinRuleSetIndex: ruleSets,
	})
	if err != nil {
		return nil, err
	}
	out := filepath.Join(cfgDir, "zoo.json")
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, res.Rendered, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("rename %s -> %s: %w", tmp, out, err)
	}
	return &res.Stats, nil
}

// scanZooBuiltins 解析 configDir 下除 zoo.json 外的所有 *.json fragment，
// 收集 outbounds[].tag 与 route.rule_set[]。无法解析的文件忽略（可能与 zoo
// 无关），不影响后续。
func scanZooBuiltins(configDir string) (tags []string, rules []RuleSetEntry, err error) {
	entries, err := os.ReadDir(configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "zoo.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(configDir, e.Name()))
		if err != nil {
			return nil, nil, err
		}
		var doc struct {
			Outbounds []struct {
				Tag string `json:"tag"`
			} `json:"outbounds"`
			Route struct {
				RuleSet []struct {
					Tag string `json:"tag"`
					URL string `json:"url"`
				} `json:"rule_set"`
			} `json:"route"`
		}
		if err := json.Unmarshal(stripJSONCLineComments(data), &doc); err != nil {
			continue
		}
		for _, ob := range doc.Outbounds {
			if ob.Tag != "" {
				tags = append(tags, ob.Tag)
			}
		}
		for _, rs := range doc.Route.RuleSet {
			rules = append(rules, RuleSetEntry{Tag: rs.Tag, URL: rs.URL})
		}
	}
	return tags, rules, nil
}

// stripJSONCLineComments 删除"首个非空白字符是 //"的整行。仓库内 JSONC fragment
// 只用这一种注释形式（无块注释、无行尾注释、无尾随逗号），因此简单实现即可。
// 字符串内的 "//"（如 URL）不受影响——它们不在行首。
func stripJSONCLineComments(b []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(b))
	for line := range bytes.SplitSeq(b, []byte("\n")) {
		if bytes.HasPrefix(bytes.TrimLeft(line, " \t"), []byte("//")) {
			continue
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}
