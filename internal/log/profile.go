package log

import "strings"

// Profile 描述终端颜色能力。
type Profile int

const (
	ProfileNone Profile = iota
	Profile8
	Profile256
	ProfileTrueColor
)

// String 返回 profile 的小写名称（与配置文件 / CLI flag 取值一致）。
func (p Profile) String() string {
	switch p {
	case Profile8:
		return "8"
	case Profile256:
		return "256"
	case ProfileTrueColor:
		return "truecolor"
	default:
		return "none"
	}
}

// ParseProfile 解析配置文件 / CLI flag 字符串；空与 "auto" 返回 (ProfileNone, true) 让上层兜底判定。
// 第二个返回值为 true 时表示需要 auto 判定。
func ParseProfile(s string) (Profile, bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return ProfileNone, true, nil
	case "none", "off", "never":
		return ProfileNone, false, nil
	case "8", "ansi", "ansi8":
		return Profile8, false, nil
	case "256", "ansi256":
		return Profile256, false, nil
	case "truecolor", "24bit", "rgb":
		return ProfileTrueColor, false, nil
	}
	return ProfileNone, false, &profileError{Value: s}
}

type profileError struct{ Value string }

func (e *profileError) Error() string {
	return "unknown color profile: " + e.Value
}

// Enabled 返回当前 profile 是否上色。
func (p Profile) Enabled() bool { return p != ProfileNone }
