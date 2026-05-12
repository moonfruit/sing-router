//go:build !linux

package cli

// redirectStderr 是 daemon 在 router(linux) 上的 fd-2 重定向钩子;
// 非 linux 平台只是为了让 darwin 等开发机能编译过测试,本函数 no-op。
// daemon 不会在 darwin 上真的跑(用户开发用),所以丢失 panic 栈也无所谓。
func redirectStderr(string) error { return nil }
