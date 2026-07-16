package ai

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	bearerPattern        = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	standaloneKeyPattern = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|AKIA[0-9A-Z]{16})\b`)
	jwtPattern           = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	secretValuePattern   = regexp.MustCompile(`(?i)(["']?(?:password|passwd|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|secret)["']?\s*[:=]\s*["']?)([^"'\s,;}]{4,})`)
	credentialURLPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`)
	privateKeyPattern    = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
)

// sanitizeProviderRequest 复制、规范化并脱敏请求；调用方原对象不会被修改。
func sanitizeProviderRequest(input ProviderRequest, maxInputBytes int) (ProviderRequest, int, int, error) {
	normalized, _, err := normalizeProviderRequest(input)
	if err != nil {
		return ProviderRequest{}, 0, 0, err
	}
	if input.MaxOutputTokens < 1 || input.MaxOutputTokens > 32_768 {
		return ProviderRequest{}, 0, 0, invalidProviderRequest("maxOutputTokens 必须在 1 到 32768 之间")
	}
	result := ProviderRequest{
		Messages: make([]Message, len(normalized.Messages)), ResponseSchema: normalized.ResponseSchema,
		Temperature: normalized.Temperature, MaxOutputTokens: normalized.MaxOutputTokens,
	}
	redactions := 0
	// Schema 正文会原样发往上游；发现凭证形态时拒绝，避免改写 const/enum 后改变合同语义。
	if _, count := redactSensitiveText(string(normalized.ResponseSchema.Schema)); count > 0 {
		return ProviderRequest{}, 0, 0, invalidProviderRequest("响应 Schema 包含疑似凭证")
	}
	description, count := redactSensitiveText(strings.TrimSpace(normalized.ResponseSchema.Description))
	redactions += count
	result.ResponseSchema.Description = description
	for messageIndex, message := range normalized.Messages {
		result.Messages[messageIndex] = Message{Role: message.Role, Parts: make([]ContentPart, len(message.Parts))}
		for partIndex, part := range message.Parts {
			switch part.Type {
			case ContentTypeText:
				text, count := redactSensitiveText(strings.TrimSpace(part.Text))
				redactions += count
				result.Messages[messageIndex].Parts[partIndex] = ContentPart{Type: ContentTypeText, Text: text}
			case ContentTypeImageURL:
				imageURL := strings.TrimSpace(part.ImageURL)
				parsed, parseErr := url.Parse(imageURL)
				if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || len(imageURL) > 4096 {
					return ProviderRequest{}, 0, 0, invalidProviderRequest("messages[%d].parts[%d] 只允许无凭据的 HTTPS 图片地址", messageIndex, partIndex)
				}
				detail := part.ImageDetail
				if detail == "" {
					detail = ImageDetailAuto
				}
				result.Messages[messageIndex].Parts[partIndex] = ContentPart{Type: ContentTypeImageURL, ImageURL: imageURL, ImageDetail: detail}
			}
		}
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return ProviderRequest{}, 0, 0, err
	}
	if len(payload) > maxInputBytes {
		return ProviderRequest{}, 0, 0, invalidProviderRequest("脱敏后的模型输入超过 %d 字节", maxInputBytes)
	}
	return result, redactions, len(payload), nil
}

func redactSensitiveText(text string) (string, int) {
	patterns := []struct {
		pattern *regexp.Regexp
		replace string
	}{
		{privateKeyPattern, "[已脱敏私钥]"},
		{bearerPattern, "Bearer [已脱敏]"},
		{standaloneKeyPattern, "[已脱敏密钥]"},
		{jwtPattern, "[已脱敏令牌]"},
		{secretValuePattern, "${1}[已脱敏]"},
		{credentialURLPattern, "${1}[已脱敏]@"},
	}
	count := 0
	for _, item := range patterns {
		matches := item.pattern.FindAllStringIndex(text, -1)
		count += len(matches)
		text = item.pattern.ReplaceAllString(text, item.replace)
	}
	return text, count
}

func invalidProviderRequest(format string, args ...any) error {
	return newProviderError(ErrorCodeInvalidRequest, fmt.Sprintf(format, args...), 0, false, 0, nil)
}
