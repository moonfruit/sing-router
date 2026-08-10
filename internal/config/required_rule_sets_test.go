package config

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moonfruit/sing-router/assets"
)

func fakeRawURL(ref, path string) string {
	return "https://example.com/raw/" + ref + "/" + path + "?access_token=tok"
}

func TestEnsureRequiredRuleSets_RemoteWhenToken(t *testing.T) {
	rd := t.TempDir()
	cfgDir := filepath.Join(rd, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 静态 fragment 不声明任何 rule_set；user zoo 也未提供
	if err := os.WriteFile(filepath.Join(cfgDir, "dns.json"), []byte(`{"route":{"rules":[{"rule_set":"GeoIP@CN"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	required := []RuleSetSource{{Tag: "GeoIP@CN", GiteePath: "rules/geoip-cn.srs", LocalRelPath: "var/rules/geoip-cn.srs"}}
	added, err := EnsureRequiredRuleSets(rd, "config", fakeRawURL, "main", required)
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
	if !strings.Contains(string(data), `"type": "remote"`) {
		t.Errorf("entry should be type:remote when rawURL provided:\n%s", data)
	}
}

func TestEnsureRequiredRuleSets_LocalFallbackWhenNoToken(t *testing.T) {
	rd := t.TempDir()
	cfgDir := filepath.Join(rd, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	required := []RuleSetSource{
		{Tag: "GeoIP@CN", GiteePath: "rules/geoip-cn.srs", LocalRelPath: "var/rules/geoip-cn.srs"},
		{Tag: "Lan", GiteePath: "rules/lan.srs", LocalRelPath: "var/rules/lan.srs"},
	}
	added, err := EnsureRequiredRuleSets(rd, "config", nil, "main", required)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 {
		t.Errorf("added = %v, want 2 entries", added)
	}
	data, err := os.ReadFile(filepath.Join(cfgDir, SupplementalRuleSetFile))
	if err != nil {
		t.Fatalf("rule-set.json missing: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"type": "local"`) {
		t.Errorf("entry should be type:local when rawURL=nil:\n%s", s)
	}
	if !strings.Contains(s, `"path": "var/rules/geoip-cn.srs"`) {
		t.Errorf("missing path for GeoIP@CN:\n%s", s)
	}
	if !strings.Contains(s, `"path": "var/rules/lan.srs"`) {
		t.Errorf("missing path for Lan:\n%s", s)
	}
	if strings.Contains(s, "access_token") {
		t.Errorf("local fallback must not embed any URL/token:\n%s", s)
	}
}

func TestEnsureRequiredRuleSets_SkipsWhenProvidedByZoo(t *testing.T) {
	rd := t.TempDir()
	cfgDir := filepath.Join(rd, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// zoo.json 已经声明了 GeoIP@CN（模拟 PreprocessZooFile 的产出）
	if err := os.WriteFile(filepath.Join(cfgDir, "zoo.json"),
		[]byte(`{"route":{"rule_set":[{"type":"remote","tag":"GeoIP@CN","url":"https://other/url"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	required := []RuleSetSource{{Tag: "GeoIP@CN", GiteePath: "rules/geoip-cn.srs"}}
	added, err := EnsureRequiredRuleSets(rd, "config", fakeRawURL, "main", required)
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
	cfgDir := filepath.Join(rd, "config")
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
	if _, err := EnsureRequiredRuleSets(rd, "config", fakeRawURL, "main", required); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, SupplementalRuleSetFile)); !os.IsNotExist(err) {
		t.Errorf("stale rule-set.json should have been removed; err=%v", err)
	}
}

func TestEnsureRequiredRuleSets_EmptyRequiredIsNoOp(t *testing.T) {
	rd := t.TempDir()
	cfgDir := filepath.Join(rd, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	added, err := EnsureRequiredRuleSets(rd, "config", fakeRawURL, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Errorf("added = %v, want empty", added)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, SupplementalRuleSetFile)); !os.IsNotExist(err) {
		t.Errorf("rule-set.json should not exist when required empty; err=%v", err)
	}
}

// 回归守护：内嵌 fragment 引用的每个 rule_set tag，要么被某个 fragment 自己定义
// （如 inline.json 的 LocalDomain），要么落在 DefaultRequiredRuleSets 里由
// EnsureRequiredRuleSets 补齐。两头都不沾 → sing-box 启动时直接
// `FATAL initialize dns router: rule-set not found: X`，daemon 陷入崩溃退避。
//
// 历史事故：dns.json 把内联 localhost 域名列表换成 rule_set "LocalDomain"、定义
// 挪进新增的 inline.json，但 install 的 fragment 白名单没同步 → 装机后 inline.json
// 缺失 → sing-box 拒绝启动。
func TestEmbeddedFragmentsRuleSetsAllResolvable(t *testing.T) {
	entries, err := fs.ReadDir(assets.FS(), "config")
	if err != nil {
		t.Fatal(err)
	}

	defined := map[string]bool{}
	referenced := map[string]string{} // tag -> 首次引用它的文件

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := assets.ReadFile("config/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(stripJSONCLineComments(data), &doc); err != nil {
			t.Fatalf("config/%s 不是合法 JSON(C): %v", e.Name(), err)
		}
		for _, tag := range collectRuleSetTags(doc, "tag", "rule_set") {
			defined[tag] = true
		}
		for _, tag := range collectRuleSetTags(doc, "rule_set", "") {
			if _, ok := referenced[tag]; !ok {
				referenced[tag] = e.Name()
			}
		}
	}

	required := map[string]bool{}
	for _, r := range DefaultRequiredRuleSets {
		required[r.Tag] = true
	}

	for tag, file := range referenced {
		if defined[tag] || required[tag] {
			continue
		}
		t.Errorf("config/%s 引用了 rule_set %q，但没有任何 fragment 定义它，"+
			"DefaultRequiredRuleSets 里也没有——sing-box 会拒绝启动", file, tag)
	}

	// 反向：DefaultRequiredRuleSets 里的条目若已经没人引用，就是该删的残留。
	for _, r := range DefaultRequiredRuleSets {
		if _, ok := referenced[r.Tag]; !ok {
			t.Errorf("DefaultRequiredRuleSets 含 %q，但内嵌 fragment 已无人引用；"+
				"若真实 zoo.json 也不需要它，应删除该条目", r.Tag)
		}
	}
}

// 回归守护：DefaultRequiredRuleSets / Makefile 的 RULE_SETS / 内嵌
// assets/var/rules/*.srs 三者必须一一对应。
//   - 少 srs：无 token 的机器上 EnsureRequiredRuleSets 写出指向不存在文件的
//     local 条目，sing-box `parse rule-set: no such file` 拒绝启动。
//   - 少 RULE_SETS：`make update-rule-sets` 悄悄漏掉它，兜底永远停在首次提交的版本。
//   - 多 srs：没人用的规则集白白撑大二进制（历史上内嵌过用不着的 lan.srs）。
func TestDefaultRequiredRuleSetsAlignment(t *testing.T) {
	want := map[string]bool{} // srs 文件名
	for _, r := range DefaultRequiredRuleSets {
		base := path.Base(r.GiteePath)
		if want[base] {
			t.Errorf("DefaultRequiredRuleSets 里 %s 重复", base)
		}
		want[base] = true
		if r.LocalRelPath != "var/rules/"+base {
			t.Errorf("rule_set %q 的 LocalRelPath=%q 与 GiteePath=%q 不一致",
				r.Tag, r.LocalRelPath, r.GiteePath)
		}
	}

	assertSameSet(t, "Makefile 的 RULE_SETS", makefileRuleSets(t), want)
	assertSameSet(t, "内嵌 assets/var/rules", embeddedRuleSetFiles(t), want)
}

func assertSameSet(t *testing.T, what string, got, want map[string]bool) {
	t.Helper()
	for f := range want {
		if !got[f] {
			t.Errorf("%s 缺少 %s（DefaultRequiredRuleSets 要它）", what, f)
		}
	}
	for f := range got {
		if !want[f] {
			t.Errorf("%s 多出 %s（DefaultRequiredRuleSets 里没人要）", what, f)
		}
	}
}

// makefileRuleSets 解析 Makefile 里 `RULE_SETS ?= a.srs b.srs` 的文件名集合。
func makefileRuleSets(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.HasPrefix(line, "RULE_SETS") {
			continue
		}
		_, rhs, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("Makefile 的 RULE_SETS 行没有 '=': %q", line)
		}
		out := map[string]bool{}
		for f := range strings.FieldsSeq(rhs) {
			out[f] = true
		}
		return out
	}
	t.Fatal("Makefile 里找不到 RULE_SETS")
	return nil
}

// embeddedRuleSetFiles 列出内嵌 var/rules 下的 *.srs（.etag 是附属产物，跳过）。
func embeddedRuleSetFiles(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := fs.ReadDir(assets.FS(), "var/rules")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".srs") {
			out[e.Name()] = true
		}
	}
	return out
}

// collectRuleSetTags 递归收集 JSON 里的 rule_set tag。
//   - key="rule_set", parentKey="": 收集所有引用点（值可以是 string 或 []string）
//   - key="tag", parentKey="rule_set": 只收集 route.rule_set[] 定义里的 tag，
//     避免把 outbound/inbound 的 tag 也算进来
func collectRuleSetTags(node any, key, parentKey string) []string {
	var out []string
	var walk func(n any, inParent bool)
	walk = func(n any, inParent bool) {
		switch v := n.(type) {
		case map[string]any:
			for k, child := range v {
				if k == key && (parentKey == "" || inParent) {
					switch s := child.(type) {
					case string:
						out = append(out, s)
					case []any:
						for _, item := range s {
							if str, ok := item.(string); ok {
								out = append(out, str)
							}
						}
					}
					continue
				}
				walk(child, parentKey != "" && k == parentKey)
			}
		case []any:
			for _, item := range v {
				walk(item, inParent)
			}
		}
	}
	walk(node, false)
	return out
}
