package datasource

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

// NewExcelHandler 注册文件上传和版本查询接口，并应用认证与对象权限。
func NewExcelHandler(authService *auth.Service, permissions *access.Service, manager *ExcelManager) http.Handler {
	mux := http.NewServeMux()
	managed := func(next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, access.Require(permissions, "DATA_SOURCE", "MANAGE", nil, next))
	}
	upload := func(reupload bool) http.Handler {
		return managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			maxBytes, err := manager.MaxFileBytes(r.Context(), claims.TenantID)
			if err != nil {
				writeDSError(w, 500, "EXCEL_QUOTA_FAILED", "failed to load upload quota")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				writeDSError(w, 400, "INVALID_EXCEL_UPLOAD", "invalid multipart upload")
				return
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				writeDSError(w, 400, "EXCEL_FILE_REQUIRED", "file is required")
				return
			}
			defer file.Close()
			extension := strings.ToLower(header.Filename[strings.LastIndex(header.Filename, ".")+1:])
			if extension != "xlsx" && extension != "xls" && extension != "csv" {
				writeDSError(w, 400, "UNSUPPORTED_FILE_FORMAT", "only .xlsx, .xls and .csv are supported")
				return
			}
			config := map[string]any{}
			if raw := r.FormValue("config"); raw != "" {
				if err := json.Unmarshal([]byte(raw), &config); err != nil {
					writeDSError(w, 400, "INVALID_EXCEL_CONFIG", "config must be a JSON object")
					return
				}
			}
			assetID := ""
			if reupload {
				assetID = r.PathValue("id")
			}
			asset, err := manager.Upload(r.Context(), claims.TenantID, assetID, header.Filename, header.Header.Get("Content-Type"), file, header.Size, config)
			if err != nil {
				writeDSError(w, 400, "EXCEL_UPLOAD_FAILED", safeFileError(err))
				return
			}
			action := "UPLOAD_FILE"
			if reupload {
				action = "UPLOAD_FILE_VERSION"
			}
			if err := manager.Audit(r.Context(), claims.TenantID, claims.Subject, action, asset.ID, map[string]any{"filename": asset.Filename, "version": asset.Version, "sizeBytes": asset.SizeBytes, "sha256": asset.SHA256}); err != nil {
				slog.ErrorContext(r.Context(), "file asset audit failed", "asset_id", asset.ID, "error", err)
			}
			writeDSJSON(w, 201, asset)
		}))
	}
	mux.Handle("POST /api/v1/excel-files", upload(false))
	mux.Handle("POST /api/v1/excel-files/{id}/versions", upload(true))
	mux.Handle("GET /api/v1/excel-files/{id}/versions", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		versions, err := manager.Versions(r.Context(), claims.TenantID, r.PathValue("id"))
		if err != nil {
			writeDSError(w, 404, "EXCEL_FILE_NOT_FOUND", "file asset not found")
			return
		}
		writeDSJSON(w, 200, map[string]any{"items": versions})
	})))
	return mux
}

// safeFileError 将内部存储错误收敛为可安全返回客户端的消息。
func safeFileError(err error) string {
	message := err.Error()
	// 仅透出可操作的校验错误，存储和数据库错误统一隐藏内部细节。
	for _, allowed := range []string{"exceeds tenant quota", "unsupported excel extension", "invalid csv", "unsupported csv encoding", "csv content is not valid", "csv GBK decoding failed", "csv GB18030 decoding failed", "csvOptions", "csv encoding", "csv delimiter", "csv quote", "csv file is empty", "formula error", "row limit", "column limit", "header row", "non-empty header", "more columns than its header", "no worksheet selected", "file asset not found"} {
		if strings.Contains(message, allowed) {
			return message
		}
	}
	return "file could not be validated or stored"
}
