// Package assets 用 //go:embed 把 sing-router 所需的全部静态资源（默认配置、
// shell 脚本、init.d 模板、firmware 钩子模板、daemon.toml 模板）打进二进制。
package assets

import (
	"embed"
	"io/fs"
)

//go:embed config.d.default daemon.toml.tmpl firmware initd rules shell var
var fsys embed.FS

// FS 返回根 fs.FS，可被 install 模块用 fs.Sub 取子树。
func FS() fs.FS { return fsys }

// MustReadFile 读取嵌入文件；不存在时 panic（属于程序员错误）。
func MustReadFile(name string) []byte {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		panic("assets.MustReadFile " + name + ": " + err.Error())
	}
	return data
}

// ReadFile 读取嵌入文件并返回 error。
func ReadFile(name string) ([]byte, error) {
	return fs.ReadFile(fsys, name)
}
