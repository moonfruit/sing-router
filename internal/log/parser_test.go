package log

import (
	"encoding/json"
	"strings"
	"testing"
)

func render(line string) string {
	ev := ParseSingBoxLine(line)
	if ev == nil {
		return ""
	}
	b, _ := json.Marshal(ev)
	return string(b)
}

func mustContain(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("expected %q in %s", sub, s)
		}
	}
}

func TestEnrich(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{`+0800 2026-04-13 16:30:05 INFO [3209467023 0ms] inbound/tun[tun-in]: inbound connection to 22.0.0.4:443`,
			[]string{`"IP":"22.0.0.4"`, `"AllIP":["22.0.0.4"]`, `"IPv4":true`, `"IPv6":false`, `"Port":443`}},
		{`+0800 2026-04-13 16:30:05 INFO [3209467023 0ms] inbound/tun[tun-in]: inbound connection from 11.0.0.1:58803`,
			[]string{`"SrcIP":"11.0.0.1"`, `"SrcPort":58803`}},
		{`+0800 2026-04-13 16:30:05 INFO router: found process path: /opt/homebrew/Cellar/gh/2.89.0/bin/gh, user: moon`,
			[]string{`"Process":"/opt/homebrew/Cellar/gh/2.89.0/bin/gh"`, `"ProcessName":"gh"`, `"ProcessUser":"moon"`}},
		{`+0800 2026-04-13 16:30:05 DEBUG router: found fakeip domain: api.github.com`,
			[]string{`"Domain":"api.github.com"`, `"FakeIP":true`}},
		{`+0800 2026-04-13 16:30:05 DEBUG [3209467023 1ms] router: sniffed protocol: tls, domain: api.github.com`,
			[]string{`"Protocol":"tls"`, `"Domain":"api.github.com"`, `"Sniffed":true`}},
		{`+0800 2026-04-13 16:30:05 INFO [3209467023 2ms] outbound/tuic[X]: outbound connection to api.github.com:443`,
			[]string{`"Domain":"api.github.com"`, `"Port":443`}},
		{`+0800 2026-04-13 16:30:05 DEBUG dns: lookup domain t7m2q9x4r8kap3-y6.edg3.org`,
			[]string{`"Domain":"t7m2q9x4r8kap3-y6.edg3.org"`}},
		{`+0800 2026-04-13 16:30:05 DEBUG dns: lookup succeed for drive.weixin.qq.com: 240e:97c:2f:1::1a 183.47.98.84 183.47.113.251`,
			[]string{`"Domain":"drive.weixin.qq.com"`, `"IP":"240e:97c:2f:1::1a"`, `"AllIP":["240e:97c:2f:1::1a","183.47.98.84","183.47.113.251"]`, `"IPv4":true`, `"IPv6":true`}},
		{`+0800 2026-04-13 16:30:05 DEBUG dns: cached A drive.weixin.qq.com. 379 IN A 183.47.98.84`,
			[]string{`"Domain":"drive.weixin.qq.com"`, `"DNS":{"QueryType":"A","TTL":379}`, `"IP":"183.47.98.84"`, `"AllIP":["183.47.98.84"]`}},
		{`+0800 2026-04-13 16:30:05 DEBUG dns: cached CNAME www.apple.com. 9 IN CNAME www-apple-com.v.aaplimg.com.`,
			[]string{`"Domain":"www.apple.com"`, `"DNS":{"QueryType":"CNAME","TTL":9,"RData":"www-apple-com.v.aaplimg.com."}`}},
		{`+0800 2026-04-13 16:30:05 DEBUG dns: cached drive.weixin.qq.com NOERROR 379`,
			[]string{`"Domain":"drive.weixin.qq.com"`, `"DNS":{"RCode":"NOERROR"}`}},
		{`+0800 2026-04-13 16:30:05 DEBUG dns: exchange api.github.com. IN A`,
			[]string{`"Domain":"api.github.com"`, `"DNS":{"QueryType":"A"}`}},
		{`+0800 2026-04-13 16:30:05 INFO [X 3ms] dns: exchanged A api.github.com. 1 IN A 22.0.0.3`,
			[]string{`"Domain":"api.github.com"`, `"DNS":{"QueryType":"A","TTL":1}`, `"IP":"22.0.0.3"`}},
		{`+0800 2026-04-13 16:30:05 DEBUG [X 3ms] dns: exchanged api.github.com NOERROR 1`,
			[]string{`"Domain":"api.github.com"`, `"DNS":{"RCode":"NOERROR"}`}},
		{`+0800 2026-04-13 17:02:00 ERROR [X 87ms] dns: exchange failed for ab.chatgpt.com. IN HTTPS: quic: transport closed: use of closed network connection`,
			[]string{`"Domain":"ab.chatgpt.com"`, `"DNS":{"QueryType":"HTTPS","Error":"quic: transport closed: use of closed network connection"}`}},
		{`+0800 2026-04-13 16:30:06 DEBUG [X 2ms] dns: response rejected for token.services.mozilla.com. IN A: response rejected: cached (cached)`,
			[]string{`"Domain":"token.services.mozilla.com"`, `"DNS":{"QueryType":"A","Error":"response rejected: cached (cached)"}`}},
		{`+0800 2026-04-13 16:30:05 DEBUG dns: lookup failed for foo.example.com: timeout`,
			[]string{`"Domain":"foo.example.com"`, `"DNS":{"Error":"timeout"}`}},
		{`+0800 2026-04-13 16:30:05 INFO [X 0ms] inbound/tun[tun-in]: inbound connection to [2001:67c:4e8:f002::a]:443`,
			[]string{`"IP":"2001:67c:4e8:f002::a"`, `"IPv4":false`, `"IPv6":true`, `"Port":443`}},
	}
	for _, c := range cases {
		got := render(c.line)
		mustContain(t, got, c.want...)
	}
}
