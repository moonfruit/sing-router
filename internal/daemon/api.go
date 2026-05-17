package daemon

import (
	"context"
	"encoding/json"
	"errors"
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

	CheckConfig  func(context.Context) error
	// Apply 是 /api/v1/apply 的入口；按 ?resource= query 决定处理范围
	// （sing-box / zoo / cn / all，默认 all）。把当前磁盘上的 staging/raw 资源
	// 真正生效（走 Applier 4 阶段；详见 Applier.Apply）。
	Apply        func(ctx context.Context, kinds []Resource) error
	StatusExtra  func() map[string]any
	ScriptByName func(name string) ([]byte, error)
	ShutdownHook func() // 通常关 ctx 让 main 退出
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
		// ?force=true 走 RestartForce 绕节流——固件钩子刚拆完 iptables 又调
		// restart 命中 2s 节流窗口时规则不会恢复；必须生效的调用方应显式置 force。
		restart := deps.Supervisor.Restart
		if r.URL.Query().Get("force") == "true" {
			restart = deps.Supervisor.RestartForce
		}
		err := restart(r.Context())
		if errors.Is(err, ErrRestartThrottled) {
			// 与"已重启"显式区分：429 Too Many Requests + sentinel code 让调用方知情。
			writeError(w, http.StatusTooManyRequests, "restart.throttled",
				err.Error()+" (pass ?force=true if this restart MUST take effect)", nil)
			return
		}
		if err != nil {
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
		if deps.Apply == nil {
			writeError(w, http.StatusNotImplemented, "not_implemented", "Apply hook not wired (auto_apply / gitee token may be missing)", nil)
			return
		}
		kinds, err := ParseResource(r.URL.Query().Get("resource"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "apply.bad_resource", err.Error(), nil)
			return
		}
		if err := deps.Apply(r.Context(), kinds); err != nil {
			writeError(w, http.StatusInternalServerError, "apply.failed", err.Error(), nil)
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
			"pid":               sup.SingBoxPID(),
			"restart_count":     sup.RestartCount(),
			"restart_in_flight": sup.RestartInFlight(),
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
