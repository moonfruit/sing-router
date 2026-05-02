package version

// Version 在编译时通过 -ldflags 注入，留空时取 "dev"。
var Version = "dev"

// String 返回带前缀的版本字符串。
func String() string {
	if Version == "" {
		return "dev"
	}
	return Version
}
