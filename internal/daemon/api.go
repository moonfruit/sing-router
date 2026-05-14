package daemon

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"time"

	"github.com/moonfruit/sing2seq/clef"
)

// APIDeps 是 HTTP handlers 依赖的接口集；测试可注入 mock。
type APIDeps struct {
	Supervisor *Supervisor
	Emitter    *clef.Emitter
	Version    string
	Rundir     string
	LogFile    string // active sing-router.log 绝对路径；通过 status 暴露给 CLI logs 默认路径推断

	// 给 reapply-rules / check / reload-cn-ipset / apply 的 hook
	ReapplyRules  func(context.Context) error
	CheckConfig   func(context.Context) error
	ReloadCNIpset func(context.Context) error // 仅重建 cn ipset,不动 iptables 规则
	ApplyPending  func(context.Context) error // 把当前磁盘上的 staging/raw 资源真正生效(CLI --apply)
	StatusExtra   func() map[string]any
	ScriptByName  func(name string) ([]byte, error)
	ShutdownHook  func() // 通常关 ctx 让 main 退出
}

// NewMux 注册所有端点到一个 http.ServeMux。
func NewMux(deps APIDeps) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, deps.statusSnapshot())
	})
	mux.HandleFunc("/api/v1/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
			return
		}
		if err := deps.Supervisor.Start(r.Context()); err != nil {
			writeError(w, http.StatusConflict, "daemon.state_conflict", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, deps.statusSnapshot())
	})
	mux.HandleFunc("/api/v1/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
			return
		}
		if err := deps.Supervisor.Stop(r.Context()); err != nil {
			writeError(w, http.StatusConflict, "daemon.state_conflict", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, deps.statusSnapshot())
	})
	mux.HandleFunc("/api/v1/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
			return
		}
		if err := deps.Supervisor.Restart(r.Context()); err != nil {
			writeError(w, http.StatusConflict, "daemon.state_conflict", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, deps.statusSnapshot())
	})
	mux.HandleFunc("/api/v1/check", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
			return
		}
		if deps.CheckConfig == nil {
			writeError(w, http.StatusNotImplemented, "not_implemented", "CheckConfig hook not wired", nil)
			return
		}
		if err := deps.CheckConfig(r.Context()); err != nil {
			writeError(w, http.StatusBadRequest, "config.singbox_check_failed", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
			return
		}
		if deps.ApplyPending == nil {
			writeError(w, http.StatusNotImplemented, "not_implemented", "ApplyPending hook not wired (auto_apply / gitee token may be missing)", nil)
			return
		}
		if err := deps.ApplyPending(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "apply.failed", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/reload-cn-ipset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
			return
		}
		if cur := deps.Supervisor.State(); cur != StateRunning {
			writeError(w, http.StatusConflict, "daemon.state_conflict", "not running: "+cur.String(), nil)
			return
		}
		if deps.ReloadCNIpset == nil {
			writeError(w, http.StatusNotImplemented, "not_implemented", "ReloadCNIpset hook not wired", nil)
			return
		}
		if err := deps.ReloadCNIpset(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "shell.reload_cn_ipset_failed", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/reapply-rules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
			return
		}
		if cur := deps.Supervisor.State(); cur != StateRunning {
			writeError(w, http.StatusConflict, "daemon.state_conflict", "not running: "+cur.String(), nil)
			return
		}
		if deps.ReapplyRules == nil {
			writeError(w, http.StatusNotImplemented, "not_implemented", "ReapplyRules hook not wired", nil)
			return
		}
		if err := deps.ReapplyRules(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "shell.startup_failed", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/script/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[len("/api/v1/script/"):]
		if deps.ScriptByName == nil {
			writeError(w, http.StatusNotImplemented, "not_implemented", "ScriptByName hook not wired", nil)
			return
		}
		data, err := deps.ScriptByName(name)
		if err != nil {
			writeError(w, http.StatusNotFound, "script.not_found", err.Error(), nil)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/api/v1/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		if deps.ShutdownHook != nil {
			go deps.ShutdownHook()
		}
	})
	return mux
}

func (deps APIDeps) statusSnapshot() map[string]any {
	sup := deps.Supervisor
	snap := map[string]any{
		"daemon": map[string]any{
			"version":  deps.Version,
			"pid":      os.Getpid(),
			"rundir":   deps.Rundir,
			"state":    sup.State().String(),
			"log_file": deps.LogFile,
		},
		"sing_box": map[string]any{
			"pid":           sup.SingBoxPID(),
			"restart_count": sup.RestartCount(),
		},
		"rules": map[string]any{
			"iptables_installed": sup.IptablesInstalled(),
		},
	}
	if deps.StatusExtra != nil {
		maps.Copy(snap, deps.StatusExtra())
	}
	return snap
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, errCode, msg string, detail any) {
	writeJSON(w, code, map[string]any{
		"error": map[string]any{
			"code":    errCode,
			"message": msg,
			"detail":  detail,
		},
	})
}

// ServeHTTP 是 daemon.go 用的薄包装；阻塞直到 ctx 取消。
func ServeHTTP(ctx context.Context, mux http.Handler, listen string) error {
	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
