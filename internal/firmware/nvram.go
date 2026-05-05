package firmware

import (
	"os/exec"
	"strings"
)

// nvramReader 是 doctor 体检 Merlin 路径时读 nvram 的最小接口。
// 生产实现 shell out 到 `nvram get`；测试实现是内存 map。
type nvramReader interface {
	Get(key string) (string, error)
}

// nvramExec 是 shellNvram 真正调用的命令；测试可替换。
var nvramExec = func(args ...string) ([]byte, error) {
	return exec.Command("nvram", args...).Output()
}

type shellNvram struct{}

func (shellNvram) Get(key string) (string, error) {
	out, err := nvramExec("get", key)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// fakeNvram 仅用于测试。
type fakeNvram map[string]string

func (f fakeNvram) Get(key string) (string, error) { return f[key], nil }
