// Package zashboard 在路由器本地生成 zashboard 的 source-ip-label-list 导入文件。
// 数据源与参考 Python 脚本 zashboard-iplabels.py 一致，但 daemon 跑在路由器上，
// 直接本地采集，无需 SSH。
package zashboard

import (
	"math/big"
	"net"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// namespace 与 Python uuid5 的命名空间常量逐字节一致，保证同一 key 得到相同 id。
var namespace = uuid.MustParse("6f1d4c2a-9b3e-5a7c-8d6f-2e4a1b0c9d8e")

// Entry 是导出给 zashboard 的单条 IP → 标签映射。
type Entry struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	ID    string `json:"id"`
}

// RawData 是 4 个本地数据源的原始文本（由 Collect 填充）。
type RawData struct {
	Clients string // nvram get custom_clientlist
	ARP     string // /proc/net/arp
	Leases  string // /var/lib/misc/dnsmasq.leases
	Neigh   string // ip -6 neigh show
}

func normMAC(mac string) string { return strings.ToUpper(strings.TrimSpace(mac)) }

func makeID(key string) string { return uuid.NewSHA1(namespace, []byte(key)).String() }

// parseCustomClientlist: 条目以 < 分隔、字段以 > 分隔，[0]=名 [1]=MAC。
func parseCustomClientlist(text string) map[string]string {
	result := map[string]string{}
	for _, entry := range strings.Split(text, "<") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		fields := strings.Split(entry, ">")
		if len(fields) < 2 {
			continue
		}
		name, mac := strings.TrimSpace(fields[0]), normMAC(fields[1])
		if name != "" && mac != "" {
			result[mac] = name
		}
	}
	return result
}

// parseARP: 列 IP HWtype Flags HWaddress Mask Device；flag 0x0 / 全零 MAC / 表头跳过。
func parseARP(text string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		cols := strings.Fields(line)
		if len(cols) < 4 || cols[0] == "IP" {
			continue
		}
		ip, flags, mac := cols[0], cols[2], normMAC(cols[3])
		if flags == "0x0" || mac == "00:00:00:00:00:00" {
			continue
		}
		if _, ok := result[mac]; !ok {
			result[mac] = ip
		}
	}
	return result
}

// parseLeases: 列 expiry MAC IP hostname clientid；setdefault。
func parseLeases(text string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		cols := strings.Fields(line)
		if len(cols) < 3 {
			continue
		}
		mac, ip := normMAC(cols[1]), cols[2]
		if _, ok := result[mac]; !ok {
			result[mac] = ip
		}
	}
	return result
}

// parseIPv6Neigh: 列含 lladdr，仅取全局单播地址（跳过 fe80 链路本地、ULA 等），去重。
func parseIPv6Neigh(text string) map[string][]string {
	result := map[string][]string{}
	for _, line := range strings.Split(text, "\n") {
		cols := strings.Fields(line)
		idx := indexOf(cols, "lladdr")
		if len(cols) < 5 || idx < 0 || idx+1 >= len(cols) {
			continue
		}
		addr := cols[0]
		ip := net.ParseIP(addr)
		if ip == nil || ip.To4() != nil || !ip.IsGlobalUnicast() || isULA(ip) {
			continue
		}
		mac := normMAC(cols[idx+1])
		if !contains(result[mac], addr) {
			result[mac] = append(result[mac], addr)
		}
	}
	return result
}

// isULA 判断 fc00::/7（Go 的 IsGlobalUnicast 对 ULA 返回 true，需额外排除）。
func isULA(ip net.IP) bool {
	v6 := ip.To16()
	return v6 != nil && ip.To4() == nil && (v6[0]&0xfe) == 0xfc
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// BuildEntries 合并 4 源 + 静态表 → 排序后的 entries。路由器数据优先，静态表补缺失 key。
func BuildEntries(raw RawData, static map[string]string) []Entry {
	names := parseCustomClientlist(raw.Clients)

	macIPv4 := map[string]string{}
	for k, v := range parseLeases(raw.Leases) { // 先 leases
		macIPv4[k] = v
	}
	for k, v := range parseARP(raw.ARP) { // ARP 优先覆盖
		macIPv4[k] = v
	}
	macIPv6 := parseIPv6Neigh(raw.Neigh)

	labels := map[string]string{}
	for mac, name := range names {
		var keys []string
		if ip, ok := macIPv4[mac]; ok {
			keys = append(keys, ip)
		}
		keys = append(keys, macIPv6[mac]...)
		for _, key := range keys {
			labels[key] = name
		}
	}
	for key, label := range static { // 静态表只补路由器缺失的 key
		if _, ok := labels[key]; !ok {
			labels[key] = label
		}
	}

	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return lessKey(keys[i], keys[j]) })

	entries := make([]Entry, 0, len(keys))
	for _, k := range keys {
		entries = append(entries, Entry{Key: k, Label: labels[k], ID: makeID(k)})
	}
	return entries
}

// lessKey: IPv4(0) < IPv6(1) < 其它(2)；同类内 IP 按数值、其它按字符串。
func lessKey(a, b string) bool {
	ca, va := keyRank(a)
	cb, vb := keyRank(b)
	if ca != cb {
		return ca < cb
	}
	if ca == 2 {
		return a < b
	}
	return va.Cmp(vb) < 0
}

func keyRank(key string) (int, *big.Int) {
	ip := net.ParseIP(key)
	if ip == nil {
		return 2, nil
	}
	if v4 := ip.To4(); v4 != nil {
		return 0, new(big.Int).SetBytes(v4)
	}
	return 1, new(big.Int).SetBytes(ip.To16())
}
