package log

import (
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ansiRe   = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	lineRe   = regexp.MustCompile(`^(?P<tz>[+-]\d{4})\s+(?P<date>\d{4}-\d{2}-\d{2})\s+(?P<time>\d{2}:\d{2}:\d{2}(?:\.\d+)?)\s+(?P<level>TRACE|DEBUG|INFO|WARN|ERROR|FATAL|PANIC)\s+(?:\[(?P<conn>\S+)\s+(?P<dur>[^]]+)]\s+)?(?P<rest>.*)$`)
	moduleRe = regexp.MustCompile(`^(?P<module>[a-zA-Z0-9_\-]+)(?:/(?P<type>[a-zA-Z0-9_\-]+))?(?:\[(?P<tag>[^]]*)])?:\s+(?P<detail>.*)$`)

	inboundToRe   = regexp.MustCompile(`^inbound(?: packet)? connection to (\[[0-9a-fA-F:]+]|[^:\s]+):(\d+)$`)
	inboundFromRe = regexp.MustCompile(`^inbound(?: packet)? connection from (\[[0-9a-fA-F:]+]|[^:\s]+):(\d+)$`)
	outboundToRe  = regexp.MustCompile(`^outbound connection to (\[[0-9a-fA-F:]+]|[^:\s]+):(\d+)$`)
	processRe     = regexp.MustCompile(`^found process path: (.+), user: (\S+)$`)
	fakeipRe      = regexp.MustCompile(`^found fakeip domain: (\S+)$`)
	sniffedRe     = regexp.MustCompile(`^sniffed(?: packet)? protocol: (\S+?)(?:, domain: (\S+))?$`)
	lookupDomRe   = regexp.MustCompile(`^lookup domain (\S+)$`)
	lookupOkRe    = regexp.MustCompile(`^lookup succeed for ([^:]+): (.+)$`)
	lookupFailRe  = regexp.MustCompile(`^lookup failed for ([^:]+): (.+)$`)
	rrRe          = regexp.MustCompile(`^(?:exchanged|cached) (\w+) (\S+?)\.? (\d+) IN \w+ (.+)$`)
	dnsStatusRe   = regexp.MustCompile(`^(?:exchanged|cached) (\S+?)\.? (\w+) (\d+)$`)
	dnsQueryRe    = regexp.MustCompile(`^(?:exchange|exchange failed for|response rejected for) (\S+?)\.? IN (\w+)(?:: (.+))?$`)
)

var levelMap = map[string]string{
	"TRACE": "Verbose",
	"DEBUG": "Debug",
	"INFO":  "Information",
	"WARN":  "Warning",
	"ERROR": "Error",
	"FATAL": "Fatal",
	"PANIC": "Fatal",
}

func stripAnsi(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

func namedMatches(re *regexp.Regexp, s string) map[string]string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(re.SubexpNames()))
	for i, name := range re.SubexpNames() {
		if name == "" {
			continue
		}
		out[name] = m[i]
	}
	return out
}

func isIP(s string) bool {
	s = strings.TrimPrefix(strings.TrimSuffix(s, "]"), "[")
	return net.ParseIP(s) != nil
}

func setHost(ev *OrderedEvent, hostKey string, ipKey string, host string) {
	if isIP(host) {
		ip := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
		ev.Set(ipKey, ip)
		setIPs(ev, []string{ip})
	} else {
		ev.Set(hostKey, host)
	}
}

func dnsSet(ev *OrderedEvent, k string, v any) {
	var dns *OrderedEvent
	if raw, ok := ev.Get("DNS"); ok {
		dns, _ = raw.(*OrderedEvent)
	}
	if dns == nil {
		dns = NewEvent()
		ev.Set("DNS", dns)
	}
	dns.Set(k, v)
}

func setIPs(ev *OrderedEvent, ips []string) {
	ev.Set("AllIP", ips)
	var hasV4, hasV6 bool
	for _, s := range ips {
		ip := net.ParseIP(strings.TrimPrefix(strings.TrimSuffix(s, "]"), "["))
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			hasV4 = true
		} else {
			hasV6 = true
		}
	}
	ev.Set("IPv4", hasV4)
	ev.Set("IPv6", hasV6)
}

func enrich(ev *OrderedEvent, detail string) {
	if m := inboundToRe.FindStringSubmatch(detail); m != nil {
		setHost(ev, "Domain", "IP", m[1])
		if p, err := strconv.Atoi(m[2]); err == nil {
			ev.Set("Port", p)
		}
		return
	}
	if m := inboundFromRe.FindStringSubmatch(detail); m != nil {
		setHost(ev, "SrcDomain", "SrcIP", m[1])
		if p, err := strconv.Atoi(m[2]); err == nil {
			ev.Set("SrcPort", p)
		}
		return
	}
	if m := outboundToRe.FindStringSubmatch(detail); m != nil {
		setHost(ev, "Domain", "IP", m[1])
		if p, err := strconv.Atoi(m[2]); err == nil {
			ev.Set("Port", p)
		}
		return
	}
	if m := processRe.FindStringSubmatch(detail); m != nil {
		ev.Set("Process", m[1])
		ev.Set("ProcessName", filepath.Base(m[1]))
		ev.Set("ProcessUser", m[2])
		return
	}
	if m := fakeipRe.FindStringSubmatch(detail); m != nil {
		ev.Set("Domain", m[1])
		ev.Set("FakeIP", true)
		return
	}
	if m := sniffedRe.FindStringSubmatch(detail); m != nil {
		ev.Set("Protocol", m[1])
		if m[2] != "" {
			ev.Set("Domain", m[2])
			ev.Set("Sniffed", true)
		}
		return
	}
	if m := lookupOkRe.FindStringSubmatch(detail); m != nil {
		ev.Set("Domain", m[1])
		ips := strings.Fields(m[2])
		if len(ips) > 0 {
			ev.Set("IP", ips[0])
			setIPs(ev, ips)
		}
		return
	}
	if m := lookupFailRe.FindStringSubmatch(detail); m != nil {
		ev.Set("Domain", m[1])
		dnsSet(ev, "Error", m[2])
		return
	}
	if m := lookupDomRe.FindStringSubmatch(detail); m != nil {
		ev.Set("Domain", m[1])
		return
	}
	if m := rrRe.FindStringSubmatch(detail); m != nil {
		ev.Set("Domain", m[2])
		dnsSet(ev, "QueryType", m[1])
		if ttl, err := strconv.Atoi(m[3]); err == nil {
			dnsSet(ev, "TTL", ttl)
		}
		rdata := m[4]
		if m[1] == "A" || m[1] == "AAAA" {
			ev.Set("IP", rdata)
			setIPs(ev, []string{rdata})
		} else {
			dnsSet(ev, "RData", rdata)
		}
		return
	}
	if m := dnsStatusRe.FindStringSubmatch(detail); m != nil {
		ev.Set("Domain", m[1])
		dnsSet(ev, "RCode", m[2])
		return
	}
	if m := dnsQueryRe.FindStringSubmatch(detail); m != nil {
		ev.Set("Domain", m[1])
		dnsSet(ev, "QueryType", m[2])
		if m[3] != "" {
			dnsSet(ev, "Error", m[3])
		}
		return
	}
}

func parseLine(raw string) *OrderedEvent {
	line := strings.TrimRight(stripAnsi(raw), "\r\n")
	if line == "" {
		return nil
	}

	m := namedMatches(lineRe, line)
	if m == nil {
		ev := NewEvent()
		ev.Set("@t", time.Now().Format("2006-01-02T15:04:05.000000-07:00"))
		ev.Set("@l", "Information")
		ev.Set("@mt", "{Raw}")
		ev.Set("Raw", line)
		ev.Set("Source", "sing-box")
		ev.Set("Parsed", false)
		return ev
	}

	tz := m["tz"]
	ts := m["date"] + "T" + m["time"] + tz[:3] + ":" + tz[3:]
	level := levelMap[m["level"]]
	if level == "" {
		level = "Information"
	}

	ev := NewEvent()
	ev.Set("@t", ts)
	ev.Set("@l", level)
	ev.Set("Source", "sing-box")

	conn, hasConn := "", false
	if v, ok := m["conn"]; ok && v != "" {
		conn = v
		hasConn = true
		if n, err := strconv.Atoi(conn); err == nil {
			ev.Set("ConnectionId", n)
		} else {
			ev.Set("ConnectionId", conn)
		}
		ev.Set("Duration", m["dur"])
	}

	rest := m["rest"]
	mm := namedMatches(moduleRe, rest)
	if mm != nil {
		ev.Set("Module", mm["module"])
		if mm["type"] != "" {
			ev.Set("Type", mm["type"])
		}
		hasTag := false
		if idx := moduleRe.SubexpIndex("tag"); idx >= 0 {
			sub := moduleRe.FindStringSubmatchIndex(rest)
			if sub != nil && sub[2*idx] >= 0 {
				hasTag = true
				ev.Set("Tag", mm["tag"])
			}
		}
		ev.Set("Detail", mm["detail"])
		enrich(ev, mm["detail"])

		var tmpl strings.Builder
		if hasConn {
			tmpl.WriteString("[{ConnectionId} {Duration}] ")
		}
		tmpl.WriteString("{Module}")
		if mm["type"] != "" {
			tmpl.WriteString("/{Type}")
		}
		if hasTag {
			tmpl.WriteString("[{Tag}]")
		}
		tmpl.WriteString(": {Detail}")
		ev.Set("@mt", tmpl.String())
	} else {
		ev.Set("Detail", rest)
		if hasConn {
			ev.Set("@mt", "[{ConnectionId} {Duration}] {Detail}")
		} else {
			ev.Set("@mt", "{Detail}")
		}
	}

	return ev
}

// ParseSingBoxLine 从 sing-box stderr 的一行（含或不含 ANSI 色码、CR/LF）解析为
// CLEF 事件。无法解析的行降级为 Parsed=false 的原始事件。返回 nil 表示空行。
func ParseSingBoxLine(line string) *OrderedEvent {
	return parseLine(line)
}
