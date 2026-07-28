package datasource

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseExcelMultipartFormAcceptsFileWithinQuota(t *testing.T) {
	body, contentType := excelMultipartRequestBody(t, bytes.Repeat([]byte("x"), 1024))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/excel-files", body)
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()

	if !parseExcelMultipartForm(response, request, 1<<20) {
		t.Fatalf("valid multipart rejected: status=%d body=%s", response.Code, response.Body)
	}
	if request.MultipartForm != nil {
		t.Cleanup(func() { _ = request.MultipartForm.RemoveAll() })
	}
	file, _, err := request.FormFile("file")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
}

func TestParseExcelMultipartFormReportsQuotaInsteadOfInvalidMultipart(t *testing.T) {
	body, contentType := excelMultipartRequestBody(t, bytes.Repeat([]byte("x"), 2<<20))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/excel-files", body)
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()

	if parseExcelMultipartForm(response, request, 1<<20) {
		t.Fatal("oversized multipart was accepted")
	}
	if response.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(response.Body.String(), `"code":"EXCEL_FILE_TOO_LARGE"`) ||
		!strings.Contains(response.Body.String(), "最多 1 MiB") ||
		strings.Contains(response.Body.String(), "invalid multipart") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func TestParseExcelMultipartFormStillRejectsMalformedPayload(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/excel-files",
		strings.NewReader("not-a-multipart-body"),
	)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	response := httptest.NewRecorder()

	if parseExcelMultipartForm(response, request, 1<<20) {
		t.Fatal("malformed multipart was accepted")
	}
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), `"code":"INVALID_EXCEL_UPLOAD"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func excelMultipartRequestBody(t *testing.T, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	file, err := writer.CreateFormFile("file", "employees.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("config", `{"skipEmptyRows":true}`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}
