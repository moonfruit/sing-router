package cli

import (
	"os"
	"syscall"
)

// redirectStderr 在 daemon 进程启动早期把内核 fd 2 dup 到 path 指向的文件。
// 这样即便 init.d/rc.func 启动 daemon 时把外层 stderr 重定向到 /dev/null,
// Go runtime 的 panic 栈 / fatal-error 仍然会落到磁盘,便于事后取证。
// 调用方传一个已确保目录存在的 path; 失败返回 error,但调用方一般忽略,
// daemon 启动不该被一次性日志重定向阻塞。
func redirectStderr(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	// 不 Close f: Close 会关闭底层 fd,而我们要让 dup 出的 fd 2 持续指向这个 inode。
	// Go GC 时若 f 被回收,*os.File 的 finalizer 会调用 close(f.Fd()),但 fd 2
	// 是另一份引用,内核引用计数不会归零;只是会让 f.Fd() 失效,这无所谓。
	return syscall.Dup3(int(f.Fd()), int(os.Stderr.Fd()), 0)
}
