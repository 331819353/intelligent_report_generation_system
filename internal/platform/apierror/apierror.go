package apierror

import "net/http"

type Error struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	RequestID  string `json:"requestId,omitempty"`
	HTTPStatus int    `json:"-"`
}

// Error 实现 error 接口，并保留稳定的机器错误码。
func (e Error) Error() string { return e.Code + ": " + e.Message }

// NotFound 构造资源不存在错误。
func NotFound(message string) Error {
	return Error{Code: "RESOURCE_NOT_FOUND", Message: message, HTTPStatus: http.StatusNotFound}
}

// Internal 返回不泄露内部实现细节的通用服务错误。
func Internal() Error {
	return Error{Code: "INTERNAL_ERROR", Message: "internal server error", HTTPStatus: http.StatusInternalServerError}
}
