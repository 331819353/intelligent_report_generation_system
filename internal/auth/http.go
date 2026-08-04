package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"intelligent-report-generation-system/internal/platform/database"
)

type Handler struct{ service *Service }

// NewHandler 注册登录、注册、刷新、登出和当前用户接口。
func NewHandler(service *Service) http.Handler {
	h := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/refresh", h.refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.Handle("PUT /api/v1/auth/domain", RequireTenantAccessToken(service, http.HandlerFunc(h.switchDomain)))
	mux.Handle("GET /api/v1/auth/me", RequireAccessToken(service, http.HandlerFunc(h.me)))
	return mux
}

// register 创建平台账号并直接签发登录令牌。
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Password    string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAuthError(w, http.StatusBadRequest, "INVALID_REQUEST", "请输入有效的注册信息")
		return
	}
	pair, err := h.service.Register(r.Context(), RegisterInput{
		Email: request.Email, DisplayName: request.DisplayName, Password: request.Password,
		RequestID: r.Header.Get("X-Request-ID"), IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidRegistration):
			writeAuthError(w, http.StatusBadRequest, "INVALID_REGISTRATION", "请填写有效的姓名和邮箱")
		case errors.Is(err, ErrRegistrationConflict):
			writeAuthError(w, http.StatusConflict, "ACCOUNT_ALREADY_EXISTS", "该邮箱已注册，请直接登录")
		case errors.Is(err, ErrWeakPassword):
			writeAuthError(w, http.StatusBadRequest, "WEAK_PASSWORD", "密码需为 10–128 位，并同时包含大小写字母和数字")
		case errors.Is(err, ErrRegistrationUnavailable):
			writeAuthError(w, http.StatusForbidden, "REGISTRATION_UNAVAILABLE", "平台未开放自助注册或默认身份尚未配置")
		default:
			writeAuthError(w, http.StatusInternalServerError, "REGISTRATION_FAILED", "注册失败，请稍后重试")
		}
		return
	}
	writeAuthJSON(w, http.StatusCreated, pair)
}

// me 返回访问令牌中的当前用户声明；内部工作区标识不暴露给客户端。
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "ACCESS_TOKEN_REQUIRED", "valid bearer token is required")
		return
	}
	writeAuthJSON(w, http.StatusOK, map[string]any{"userId": claims.Subject, "tokenVersion": claims.TokenVersion})
}

// login 解析登录请求并把客户端环境信息交给认证服务审计。
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAuthError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if strings.TrimSpace(request.Email) == "" || request.Password == "" {
		writeAuthError(w, http.StatusBadRequest, "INVALID_REQUEST", "email and password are required")
		return
	}
	pair, err := h.service.Login(r.Context(), LoginInput{Email: request.Email, Password: request.Password, RequestID: r.Header.Get("X-Request-ID"), IPAddress: clientIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			writeAuthError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "account or password is invalid")
			return
		}
		writeAuthError(w, http.StatusInternalServerError, "AUTHENTICATION_FAILED", "authentication service failed")
		return
	}
	writeAuthJSON(w, http.StatusOK, pair)
}

// switchDomain 在服务端同步当前会话领域；停用或撤权时会话退回无领域控制面。
func (h *Handler) switchDomain(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "ACCESS_TOKEN_REQUIRED", "valid bearer token is required")
		return
	}
	var request struct {
		DomainID string `json:"domainId"`
	}
	if err := decodeJSON(w, r, &request); err != nil || strings.TrimSpace(request.DomainID) == "" {
		writeAuthError(w, http.StatusBadRequest, "INVALID_REQUEST", "domainId is required")
		return
	}
	if _, err := h.service.SwitchBusinessDomain(
		r.Context(), claims, strings.TrimSpace(request.DomainID),
	); err != nil {
		if errors.Is(err, ErrDomainForbidden) {
			writeAuthError(
				w, http.StatusForbidden, "BUSINESS_DOMAIN_FORBIDDEN",
				"selected business domain is not available to this user",
			)
			return
		}
		writeAuthError(w, http.StatusInternalServerError, "DOMAIN_SWITCH_FAILED", "failed to switch business domain")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

func requireAccessToken(
	service *Service, useRequestedDomain bool, next http.Handler,
) http.Handler {
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
		session, err := service.ValidateAccessSession(r.Context(), claims)
		if err != nil {
			if errors.Is(err, ErrDomainForbidden) {
				writeAuthError(
					w, http.StatusUnauthorized, "BUSINESS_DOMAIN_SESSION_DISABLED",
					"the business domain bound to this session is no longer available",
				)
				return
			}
			writeAuthError(w, http.StatusUnauthorized, "REVOKED_ACCESS_TOKEN", "access token has been revoked")
			return
		}
		domainID := session.DomainID
		if useRequestedDomain {
			requestedDomainID := strings.TrimSpace(r.Header.Get("X-Business-Domain-ID"))
			if requestedDomainID != "" {
				domainID, err = service.ResolveBusinessDomain(
					r.Context(), claims.TenantID, claims.Subject, requestedDomainID,
				)
				if err != nil {
					writeAuthError(
						w, http.StatusForbidden, "BUSINESS_DOMAIN_FORBIDDEN",
						"selected business domain is not available to this user",
					)
					return
				}
			}
		}
		if useRequestedDomain && domainID == "" {
			writeAuthError(
				w, http.StatusForbidden, "BUSINESS_DOMAIN_REQUIRED",
				"an active business domain membership is required",
			)
			return
		}
		ctx := context.WithValue(r.Context(), claimsKey{}, claims)
		ctx = database.WithAccessContext(ctx, claims.Subject, domainID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAccessToken 验证 Bearer 令牌、会话和请求指定的业务领域。
func RequireAccessToken(service *Service, next http.Handler) http.Handler {
	return requireAccessToken(service, true, next)
}

// RequireTenantAccessToken 用于租户管理控制面：忽略客户端领域请求头。
// 无领域用户仍可登录并在控制面申请领域，但无法进入任何数据面接口。
func RequireTenantAccessToken(service *Service, next http.Handler) http.Handler {
	return requireAccessToken(service, false, next)
}
