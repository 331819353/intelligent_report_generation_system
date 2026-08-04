package datasourceai

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

var (
	markdownFieldPattern    = regexp.MustCompile(`(?m)^\s*\|\s*([^|\r\n]+?)\s*\|\s*([^|\r\n]+?)\s*\|?\s*$`)
	inlineFieldPattern      = regexp.MustCompile(`(?m)^\s*[-*]?\s*([^:=：|\r\n]{1,40}?)\s*[:=：]\s*(.+?)\s*$`)
	markdownPasswordPattern = regexp.MustCompile("(?im)(\\|\\s*(?:密码|口令|password|passwd)\\s*\\|\\s*`?)([^`|\\r\\n]{1,})(`?\\s*\\|)")
	inlinePasswordPattern   = regexp.MustCompile("(?im)((?:密码|口令|password|passwd)\\s*[:=：]\\s*[\\\"'`]?)([^\\\"'`\\r\\n|]{1,})([\\\"'`]?)")
	nonCodeCharacterPattern = regexp.MustCompile(`[^a-z0-9]+`)
	repeatedUnderscore      = regexp.MustCompile(`_+`)
	naturalHostPattern      = regexp.MustCompile("(?i)(?:host|主机|地址)\\s*(?:为|是|[:：=])?\\s*[`'\"]?([A-Za-z0-9.-]+)")
	naturalPortPattern      = regexp.MustCompile("(?i)(?:port|端口)\\s*(?:为|是|[:：=])?\\s*[`'\"]?([0-9]{1,5})")
	naturalDatabasePattern  = regexp.MustCompile("(?i)(?:database(?:/service)?|service(?:[ _-]?name)?|数据库|服务名?|库名)\\s*(?:为|是|[:：=])?\\s*[`'\"]?([A-Za-z0-9_$#.-]+)")
	naturalSIDPattern       = regexp.MustCompile("(?i)(?:oracle\\s*)?sid\\s*(?:为|是|[:：=])?\\s*[`'\"]?([A-Za-z0-9_$#.-]+)")
	naturalUsernamePattern  = regexp.MustCompile("(?i)(?:username|user|用户名|用户|账号|账户)\\s*(?:为|是|[:：=])?\\s*[`'\"]?([A-Za-z0-9_$#.@-]+)")
)

type parsedInstruction struct {
	Draft             Draft
	RecognizedFields  int
	PasswordMentioned bool
}

func redactInstructionSecrets(value string) (string, bool) {
	mentioned := markdownPasswordPattern.MatchString(value) || inlinePasswordPattern.MatchString(value)
	value = markdownPasswordPattern.ReplaceAllString(value, "${1}[已转入安全输入]${3}")
	value = inlinePasswordPattern.ReplaceAllString(value, "${1}[已转入安全输入]${3}")
	return value, mentioned
}

func parseStructuredInstruction(instruction string, base Draft) parsedInstruction {
	result := parsedInstruction{Draft: base}
	seen := map[string]bool{}
	apply := func(rawKey, rawValue string) {
		key := normalizeStructuredKey(rawKey)
		value := cleanStructuredValue(rawValue)
		if value == "" {
			return
		}
		switch key {
		case "type", "类型", "数据库类型", "数据源类型":
			if sourceType := structuredSourceType(value); sourceType != "" {
				result.Draft.Type = sourceType
				seen["type"] = true
			}
		case "host", "hostname", "server", "ip", "地址", "主机", "主机地址", "服务器":
			result.Draft.Host = value
			seen["host"] = true
		case "port", "端口":
			if port, err := strconv.Atoi(value); err == nil && port > 0 && port <= 65535 {
				result.Draft.Port = port
				seen["port"] = true
			}
		case "database", "database/service", "databaseservice", "数据库", "数据库/服务名", "库名":
			result.Draft.Database = value
			seen["database"] = true
		case "service", "service_name", "servicename", "服务名":
			result.Draft.Database = value
			result.Draft.OracleConnectMode = "SERVICE_NAME"
			seen["database"] = true
			seen["oracleConnectMode"] = true
		case "sid", "oracle_sid", "oraclesid":
			result.Draft.Database = value
			result.Draft.OracleConnectMode = "SID"
			seen["database"] = true
			seen["oracleConnectMode"] = true
		case "oracleconnectmode", "oracle_connect_mode", "连接模式", "oracle连接模式":
			if strings.Contains(strings.ToUpper(value), "SID") {
				result.Draft.OracleConnectMode = "SID"
			} else if strings.Contains(strings.ToUpper(value), "SERVICE") || strings.Contains(value, "服务名") {
				result.Draft.OracleConnectMode = "SERVICE_NAME"
			}
			seen["oracleConnectMode"] = true
		case "schema", "模式":
			if result.Draft.Database == "" {
				result.Draft.Database = value
				seen["database"] = true
			}
		case "username", "user", "用户名", "用户", "账号", "账户":
			result.Draft.Username = value
			seen["username"] = true
		case "password", "passwd", "密码", "口令":
			result.PasswordMentioned = true
			seen["password"] = true
		case "name", "名称", "数据源名称":
			result.Draft.Name = value
			seen["name"] = true
		case "code", "编码", "数据源编码":
			result.Draft.Code = value
			seen["code"] = true
		}
	}
	for _, match := range markdownFieldPattern.FindAllStringSubmatch(instruction, -1) {
		apply(match[1], match[2])
	}
	for _, match := range inlineFieldPattern.FindAllStringSubmatch(instruction, -1) {
		apply(match[1], match[2])
	}
	for _, item := range []struct {
		key     string
		pattern *regexp.Regexp
	}{
		{"host", naturalHostPattern},
		{"port", naturalPortPattern},
		{"database", naturalDatabasePattern},
		{"sid", naturalSIDPattern},
		{"username", naturalUsernamePattern},
	} {
		if match := item.pattern.FindStringSubmatch(instruction); len(match) == 2 {
			apply(item.key, match[1])
		}
	}
	if sourceType := structuredSourceType(instruction); sourceType != "" {
		result.Draft.Type = sourceType
		seen["type"] = true
	}
	result.RecognizedFields = len(seen)
	return result
}

func normalizeStructuredKey(value string) string {
	value = strings.ToLower(cleanStructuredValue(value))
	return strings.NewReplacer(" ", "", "\t", "", "-", "_", "／", "/").Replace(value)
}

func cleanStructuredValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "`'\" ")
	return strings.TrimSpace(value)
}

func structuredSourceType(value string) string {
	value = strings.ToLower(value)
	switch {
	case strings.Contains(value, "mysql"):
		return "MYSQL"
	case strings.Contains(value, "oracle"):
		return "ORACLE"
	case strings.Contains(value, "excel") || strings.Contains(value, "csv"):
		return "EXCEL"
	default:
		return ""
	}
}

func localTurnResult(parsed parsedInstruction, mode string, passwordProvided, fileProvided bool) (TurnResult, error) {
	draft, fixes := safeRepairs(parsed.Draft, nil)
	draft, identityFixes := ensureGeneratedIdentity(draft, mode == "CREATE")
	fixes = uniqueStrings(append(fixes, identityFixes...))
	if err := validateDraft(draft); err != nil {
		return TurnResult{}, fmt.Errorf("%w: %v", ErrInvalidOutput, err)
	}
	missing := missingFields(draft, mode, passwordProvided, fileProvided)
	action := "TEST"
	if len(missing) > 0 {
		action = "ASK"
	}
	reply := "已从你提供的参数中识别出数据源配置。"
	if parsed.PasswordMentioned && mode == "CREATE" && !passwordProvided {
		reply += "聊天中的密码内容已被清除，请在下方安全区域重新填写一次。"
	} else if len(missing) > 0 {
		reply += "请继续补充缺少的信息。"
	} else {
		reply += "信息已齐全，可以测试连接。"
	}
	return TurnResult{
		Reply: reply, Draft: draft, MissingFields: missing,
		ReadyToTest: len(missing) == 0, SuggestedAction: action,
		SuggestedChecks: []string{}, AutoFixes: fixes,
	}, nil
}

func ensureGeneratedIdentity(value Draft, force bool) (Draft, []string) {
	if value.Type == "EXCEL" || value.Type == "" || value.Host == "" || value.Port == 0 ||
		value.Database == "" || value.Username == "" {
		return value, nil
	}
	raw := fmt.Sprintf("%s_%s:%d_%s_%s", strings.ToUpper(value.Type), value.Host, value.Port, value.Database, value.Username)
	generatedName := cleanText(raw, 200)
	codeBase := nonCodeCharacterPattern.ReplaceAllString(strings.ToLower(raw), "_")
	codeBase = strings.Trim(repeatedUnderscore.ReplaceAllString(codeBase, "_"), "_")
	if codeBase == "" {
		codeBase = "source"
	}
	generatedCode := "ds_" + codeBase
	if len(generatedCode) > 128 {
		sum := sha256.Sum256([]byte(raw))
		suffix := hex.EncodeToString(sum[:])[:12]
		generatedCode = strings.TrimRight(generatedCode[:115], "_") + "_" + suffix
	}
	fixes := make([]string, 0, 2)
	if force || value.Name == "" {
		if value.Name != generatedName {
			value.Name = generatedName
			fixes = append(fixes, "已按类型、地址、数据库/服务名和用户名自动生成名称")
		}
	}
	if force || value.Code == "" {
		if value.Code != generatedCode {
			value.Code = generatedCode
			fixes = append(fixes, "已自动生成数据源编码")
		}
	}
	return value, fixes
}

func generatedIdentityReply(missing []string) string {
	prefix := "已根据连接信息自动生成数据源名称和编码。"
	if len(missing) == 0 {
		return prefix + "信息已齐全，可以测试连接。"
	}
	if len(missing) == 1 && missing[0] == "password" {
		return prefix + "请在下方安全区域填写数据库密码。"
	}
	return prefix + "请继续补充尚缺少的连接信息。"
}

func canUseLocalFallback(err error) bool {
	var providerErr *aiplatform.ProviderError
	if !errors.As(err, &providerErr) {
		return errors.Is(err, ErrInvalidOutput)
	}
	switch providerErr.Code {
	case aiplatform.ErrorCodeInvalidOutput, aiplatform.ErrorCodeInvalidResponse,
		aiplatform.ErrorCodeTimeout, aiplatform.ErrorCodeProviderUnavailable:
		return true
	default:
		return false
	}
}
