package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/platform/apierror"
	"intelligent-report-generation-system/internal/platform/requestid"
)

const requestIDHeader = "X-Request-ID"

type Server struct {
	httpServer *http.Server
	logger     *slog.Logger
}

// New 创建包含健康检查、业务路由和通用中间件的 HTTP 服务。
func New(cfg config.Config, logger *slog.Logger, businessHandler ...http.Handler) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", healthHandler("live"))
	mux.HandleFunc("GET /health/ready", healthHandler("ready"))
	if len(businessHandler) > 0 && businessHandler[0] != nil {
		mux.Handle("/api/", businessHandler[0])
	}

	s := &Server{logger: logger}
	s.httpServer = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           s.middleware(mux),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
	return s
}

// ListenAndServe 开始监听配置的 HTTP 地址。
func (s *Server) ListenAndServe() error { return s.httpServer.ListenAndServe() }

// Shutdown 在给定上下文期限内优雅关闭 HTTP 服务。
func (s *Server) Shutdown(ctx context.Context) error { return s.httpServer.Shutdown(ctx) }

// Handler 暴露完整路由，供测试或上层服务复用。
func (s *Server) Handler() http.Handler { return s.httpServer.Handler }

// healthHandler 返回固定状态的轻量健康检查处理器。
func healthHandler(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": status})
	}
}

// middleware 统一注入请求 ID、访问日志与异常恢复逻辑。
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = requestid.New()
		}
		w.Header().Set(requestIDHeader, id)
		r = r.WithContext(requestid.WithContext(r.Context(), id))

		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("http panic", "request_id", id, "panic", recovered, "stack", string(debug.Stack()))
				err := apierror.Internal()
				err.RequestID = id
				writeJSON(w, err.HTTPStatus, err)
			}
			s.logger.Info("http request", "request_id", id, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(startedAt).Milliseconds())
		}()

		next.ServeHTTP(w, r)
	})
}

// writeJSON 以统一内容类型和状态码输出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
