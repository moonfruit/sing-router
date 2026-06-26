package zashboard

import (
	"reflect"
	"testing"
)

func TestParseCustomClientlist(t *testing.T) {
	// 条目以 < 分隔，字段以 > 分隔，[0]=名 [1]=MAC
	in := "<💻笔记本>AA:BB:CC:DD:EE:01>0>><📱手机>aa:bb:cc:dd:ee:02>0>"
	got := parseCustomClientlist(in)
	want := map[string]string{
		"AA:BB:CC:DD:EE:01": "💻笔记本",
		"AA:BB:CC:DD:EE:02": "📱手机",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseARP(t *testing.T) {
	// 列: IP HWtype Flags HWaddress Mask Device；跳过表头/flags 0x0/全零 MAC
	in := "IP address       HW type     Flags       HW address            Mask     Device\n" +
		"192.168.50.10    0x1         0x2         aa:bb:cc:dd:ee:01     *        br0\n" +
		"192.168.50.11    0x1         0x0         aa:bb:cc:dd:ee:02     *        br0\n" +
		"192.168.50.12    0x1         0x2         00:00:00:00:00:00     *        br0\n"
	got := parseARP(in)
	want := map[string]string{"AA:BB:CC:DD:EE:01": "192.168.50.10"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseLeases(t *testing.T) {
	// 列: expiry MAC IP hostname clientid；setdefault（首次出现优先）
	in := "1700000000 aa:bb:cc:dd:ee:01 192.168.50.20 host-a 01:aa\n" +
		"1700000001 aa:bb:cc:dd:ee:01 192.168.50.99 host-a 01:aa\n"
	got := parseLeases(in)
	want := map[string]string{"AA:BB:CC:DD:EE:01": "192.168.50.20"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseIPv6NeighGlobalOnly(t *testing.T) {
	// 仅取全局单播；跳过 fe80 链路本地、fd00 ULA；去重
	in := "2408:1:2:3::abcd dev br0 lladdr aa:bb:cc:dd:ee:01 REACHABLE\n" +
		"fe80::1 dev br0 lladdr aa:bb:cc:dd:ee:01 STALE\n" +
		"fd00::1 dev br0 lladdr aa:bb:cc:dd:ee:01 STALE\n" +
		"2408:1:2:3::abcd dev br0 lladdr aa:bb:cc:dd:ee:01 REACHABLE\n"
	got := parseIPv6Neigh(in)
	want := map[string][]string{"AA:BB:CC:DD:EE:01": {"2408:1:2:3::abcd"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMakeIDKnownVectors(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1":         "25889ea6-b3f0-5e91-940e-0949be5e7472",
		"192.168.50.1":      "07e198c4-5060-5d0b-bae6-03094a177617",
		"fdfe:dcba:9876::1": "68382dcd-409e-5a1b-bb06-9bea25eaeeed",
	}
	for key, want := range cases {
		if got := makeID(key); got != want {
			t.Fatalf("makeID(%q)=%s want %s", key, got, want)
		}
	}
}

func TestBuildEntriesMergeAndSort(t *testing.T) {
	raw := RawData{
		Clients: "<💻笔记本>AA:BB:CC:DD:EE:01>0>",
		Leases:  "1700000000 aa:bb:cc:dd:ee:01 192.168.50.20 host-a 01:aa\n",
		ARP: "IP address HW type Flags HW address Mask Device\n" +
			"192.168.50.10 0x1 0x2 aa:bb:cc:dd:ee:01 * br0\n",
		Neigh: "2408:1:2:3::1 dev br0 lladdr aa:bb:cc:dd:ee:01 REACHABLE\n",
	}
	static := map[string]string{
		"127.0.0.1":     "💻本机",
		"192.168.50.10": "应被路由器数据覆盖",
	}
	got := BuildEntries(raw, static)

	// 期望: ARP 覆盖 leases → MAC 的 IPv4=192.168.50.10；含 IPv6；静态 127.0.0.1 补入；
	//       192.168.50.10 用路由器名（路由器优先）。排序: IPv4(127.0.0.1 < 192.168.50.10) < IPv6
	want := []Entry{
		{Key: "127.0.0.1", Label: "💻本机", ID: makeID("127.0.0.1")},
		{Key: "192.168.50.10", Label: "💻笔记本", ID: makeID("192.168.50.10")},
		{Key: "2408:1:2:3::1", Label: "💻笔记本", ID: makeID("2408:1:2:3::1")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}
