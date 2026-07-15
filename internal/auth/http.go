package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
)

type Handler struct{ service *Service }

// NewHandler 注册登录、刷新、登出和当前用户接口。
func NewHandler(service *Service) http.Handler {
	h := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/refresh", h.refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.Handle("GET /api/v1/auth/me", RequireAccessToken(service, http.HandlerFunc(h.me)))
	return mux
}

// me 返回访问令牌中的当前身份声明。
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "ACCESS_TOKEN_REQUIRED", "valid bearer token is required")
		return
	}
	writeAuthJSON(w, http.StatusOK, map[string]any{"userId": claims.Subject, "tenantId": claims.TenantID, "tokenVersion": claims.TokenVersion})
}

// login 解析登录请求并把客户端环境信息交给认证服务审计。
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TenantCode string `json:"tenantCode"`
		Email      string `json:"email"`
		Password   string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAuthError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if strings.TrimSpace(request.TenantCode) == "" || strings.TrimSpace(request.Email) == "" || request.Password == "" {
		writeAuthError(w, http.StatusBadRequest, "INVALID_REQUEST", "tenantCode, email and password are required")
		return
	}
	pair, err := h.service.Login(r.Context(), LoginInput{TenantCode: request.TenantCode, Email: request.Email, Password: request.Password, RequestID: r.Header.Get("X-Request-ID"), IPAddress: clientIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			writeAuthError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "tenant, account or password is invalid")
			return
		}
		writeAuthError(w, http.StatusInternalServerError, "AUTHENTICATION_FAILED", "authentication service failed")
		return
	}
	writeAuthJSON(w, http.StatusOK, pair)
}

// refresh 轮换刷新令牌并返回新的令牌对。
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(w, r, &request); err != nil || request.RefreshToken == "" {
		writeAuthError(w, http.StatusBadRequest, "INVALID_REQUEST", "refreshToken is required")
		return
	}
	pair, err := h.service.Refresh(r.Context(), request.RefreshToken)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "refresh token is invalid or expired")
		return
	}
	writeAuthJSON(w, http.StatusOK, pair)
}

// logout 撤销当前刷新会话。
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(w, r, &request); err != nil || request.RefreshToken == "" {
		writeAuthError(w, http.StatusBadRequest, "INVALID_REQUEST", "refreshToken is required")
		return
	}
	if err := h.service.Logout(r.Context(), request.RefreshToken); err != nil {
		writeAuthError(w, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "refresh token is invalid or already revoked")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeJSON 严格解码请求体，拒绝未声明字段。
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// writeAuthJSON 输出认证模块的 JSON 响应。
func writeAuthJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeAuthError 输出带请求 ID 的稳定错误结构。
func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	writeAuthJSON(w, status, map[string]string{"code": code, "message": message})
}

// clientIP 优先读取反向代理传递的客户端地址，并回退到连接地址。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return ""
}

type claimsKey struct{}

// ClaimsFromContext 获取认证中间件写入的访问声明。
func ClaimsFromContext(ctx context.Context) (AccessClaims, bool) {
	claims, ok := ctx.Value(claimsKey{}).(AccessClaims)
	return claims, ok
}

// RequireAccessToken 验证 Bearer 令牌及服务端会话后再放行业务请求。
func RequireAccessToken(service *Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeAuthError(w, http.StatusUnauthorized, "ACCESS_TOKEN_REQUIRED", "valid bearer token is required")
			return
		}
		claims, err := service.tokens.Parse(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "INVALID_ACCESS_TOKEN", "access token is invalid or expired")
			return
		}
		if err := service.ValidateAccess(r.Context(), claims); err != nil {
			writeAuthError(w, http.StatusUnauthorized, "REVOKED_ACCESS_TOKEN", "access token has been revoked")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsKey{}, claims)))
	})
}
