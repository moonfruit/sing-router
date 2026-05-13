package log

import (
	"fmt"
	"math"
)

const ansiReset = "\x1b[0m"

// LevelColorPrefix 返回 level 在指定 profile 下的 ANSI 前缀。profile=none 时返回空串。
func LevelColorPrefix(profile Profile, lvl Level) string {
	if !profile.Enabled() {
		return ""
	}
	switch profile {
	case Profile8:
		return level8[lvl]
	case Profile256:
		return level256[lvl]
	case ProfileTrueColor:
		return levelTrueColor[lvl]
	}
	return ""
}

// ColorReset 返回 ANSI reset，profile=none 时返回空串。
func ColorReset(profile Profile) string {
	if profile.Enabled() {
		return ansiReset
	}
	return ""
}

// ConnPalette 返回 ConnectionId 调色板（高亮显眼色），按 profile 长度不同。
// profile=none 返回 nil。
func ConnPalette(profile Profile) []string {
	switch profile {
	case Profile8:
		return connPalette8
	case Profile256:
		return connPalette256
	case ProfileTrueColor:
		return connPaletteTrueColor
	}
	return nil
}

// --- level palettes ---

var level8 = map[Level]string{
	LevelTrace: "\x1b[90m",   // bright black (grey)
	LevelDebug: "\x1b[36m",   // cyan
	LevelInfo:  "\x1b[32m",   // green
	LevelWarn:  "\x1b[33m",   // yellow
	LevelError: "\x1b[31m",   // red
	LevelFatal: "\x1b[1;31m", // bold red
}

var level256 = map[Level]string{
	LevelTrace: "\x1b[38;5;244m",
	LevelDebug: "\x1b[38;5;51m",
	LevelInfo:  "\x1b[38;5;46m",
	LevelWarn:  "\x1b[38;5;226m",
	LevelError: "\x1b[38;5;196m",
	LevelFatal: "\x1b[1;38;5;197m",
}

var levelTrueColor = map[Level]string{
	LevelTrace: "\x1b[38;2;136;136;136m",
	LevelDebug: "\x1b[38;2;0;215;255m",
	LevelInfo:  "\x1b[38;2;95;255;95m",
	LevelWarn:  "\x1b[38;2;255;255;95m",
	LevelError: "\x1b[38;2;255;95;95m",
	LevelFatal: "\x1b[1;38;2;255;0;95m",
}

// --- conn palettes ---

// 8 色：6 个非黑非白的基础色。
var connPalette8 = []string{
	"\x1b[91m", // bright red
	"\x1b[92m", // bright green
	"\x1b[93m", // bright yellow
	"\x1b[94m", // bright blue
	"\x1b[95m", // bright magenta
	"\x1b[96m", // bright cyan
}

// 256 色：从 xterm-256 高亮区域手选 16 个显眼色（避开 0-15 基础与 232-255 灰阶）。
var connPalette256 = []string{
	"\x1b[38;5;39m",  // sky blue
	"\x1b[38;5;46m",  // green
	"\x1b[38;5;82m",  // lime
	"\x1b[38;5;118m", // chartreuse
	"\x1b[38;5;154m", // greenyellow
	"\x1b[38;5;190m", // yellow
	"\x1b[38;5;208m", // orange
	"\x1b[38;5;202m", // dark orange
	"\x1b[38;5;196m", // red
	"\x1b[38;5;199m", // hot pink
	"\x1b[38;5;201m", // magenta
	"\x1b[38;5;165m", // purple
	"\x1b[38;5;129m", // violet
	"\x1b[38;5;93m",  // indigo
	"\x1b[38;5;51m",  // cyan
	"\x1b[38;5;87m",  // light cyan
}

// truecolor：HSL 空间均匀采样 24 色，S=0.80、L=0.60，避开纯灰；启动时构造一次。
var connPaletteTrueColor = makeTrueColorPalette(24)

func makeTrueColorPalette(n int) []string {
	out := make([]string, 0, n)
	const s = 0.80
	const l = 0.60
	for i := range n {
		h := float64(i) / float64(n) * 360.0
		r, g, b := hslToRGB(h, s, l)
		out = append(out, fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b))
	}
	return out
}

// hslToRGB 把 HSL（H 度，S/L 0..1）转成 0..255 的 sRGB。
func hslToRGB(h, s, l float64) (int, int, int) {
	c := (1 - math.Abs(2*l-1)) * s
	hp := h / 60.0
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r1, g1, b1 float64
	switch {
	case hp < 1:
		r1, g1, b1 = c, x, 0
	case hp < 2:
		r1, g1, b1 = x, c, 0
	case hp < 3:
		r1, g1, b1 = 0, c, x
	case hp < 4:
		r1, g1, b1 = 0, x, c
	case hp < 5:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	m := l - c/2
	return clamp8(r1 + m), clamp8(g1 + m), clamp8(b1 + m)
}

func clamp8(v float64) int {
	x := int(math.Round(v * 255))
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return x
}
