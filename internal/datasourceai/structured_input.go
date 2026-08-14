package datasourceai

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/datasource"
)

var (
	markdownFieldPattern      = regexp.MustCompile(`(?m)^\s*\|\s*([^|\r\n]+?)\s*\|\s*([^|\r\n]+?)\s*\|?\s*$`)
	inlineFieldPattern        = regexp.MustCompile(`(?m)^\s*[-*]?\s*([^:=：|\r\n]{1,40}?)\s*[:=：]\s*(.+?)\s*$`)
	markdownPasswordPattern   = regexp.MustCompile("(?im)(\\|\\s*(?:密码|口令|password|passwd)\\s*\\|\\s*`?)([^`|\\r\\n]{1,})(`?\\s*\\|)")
	inlinePasswordPattern     = regexp.MustCompile("(?im)((?:密码|口令|password|passwd)\\s*[:=：]\\s*[\\\"'`]?)([^\\\"'`\\r\\n|]{1,})([\\\"'`]?)")
	uriPasswordPattern        = regexp.MustCompile(`(?i)(://[^/@\s:]+:)([^@/\s]+)(@)`)
	connectionURIPattern      = regexp.MustCompile("(?i)(?:jdbc:)?(?:mysql|mariadb|postgresql|postgres|oracle|sqlserver|mssql|clickhouse)://[^\\s\\\"'`]+")
	oracleJDBCThinPattern     = regexp.MustCompile(`(?i)jdbc:oracle:thin:@(?:/{2})?([A-Za-z0-9.-]+):([0-9]{1,5})[/:]([A-Za-z0-9_$#.-]+)`)
	sqlServerJDBCPattern      = regexp.MustCompile(`(?i)jdbc:sqlserver://([A-Za-z0-9.-]+)(?::([0-9]{1,5}))?(?:;[^\s]+)?`)
	sqlServerDatabasePattern  = regexp.MustCompile(`(?i)(?:^|;)database(?:name)?=([^;\s]+)`)
	nonCodeCharacterPattern   = regexp.MustCompile(`[^a-z0-9]+`)
	repeatedUnderscore        = regexp.MustCompile(`_+`)
	naturalHostPattern        = regexp.MustCompile("(?i)(?:host|主机|地址)\\s*(?:为|是|[:：=])?\\s*[`'\"]?([A-Za-z0-9.-]+)")
	naturalPortPattern        = regexp.MustCompile("(?i)(?:port|端口)\\s*(?:为|是|[:：=])?\\s*[`'\"]?([0-9]{1,5})")
	naturalDatabasePattern    = regexp.MustCompile("(?i)(?:database(?:[ _-]?name|/service(?:[ _-]?name)?)?|(?:oracle\\s*)?service(?:[ _-]?name)?|数据库(?:名称)?|服务(?:名(?:称)?)?|库名)\\s*(?:为|是|[:：=])?\\s*[`'\"]?([A-Za-z0-9_$#.-]+)")
	naturalSIDPattern         = regexp.MustCompile("(?i)(?:oracle\\s*)?sid\\s*(?:为|是|[:：=])?\\s*[`'\"]?([A-Za-z0-9_$#.-]+)")
	naturalUsernamePattern    = regexp.MustCompile("(?i)(?:username|user|用户名|用户|账号|账户)\\s*(?:为|是|[:：=])?\\s*[`'\"]?([A-Za-z0-9_$#.@-]+)")
	positionalEndpointPattern = regexp.MustCompile(`^\s*([A-Za-z0-9.-]+)\s*[:：]\s*([0-9]{1,5})\s*[/／]\s*([A-Za-z0-9_$#.-]+)\s*$`)
	positionalUsernamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_$#.@-]{0,127}$`)
)

type parsedInstruction struct {
	Draft             Draft
	RecognizedFields  int
	Recognized        map[string]bool
	PasswordMentioned bool
}

func redactInstructionSecrets(value string) (string, bool) {
	mentioned := markdownPasswordPattern.MatchString(value) || inlinePasswordPattern.MatchString(value)
	if uriPasswordPattern.MatchString(value) {
		mentioned = true
	}
	value = markdownPasswordPattern.ReplaceAllString(value, "${1}[已转入安全输入]${3}")
	value = inlinePasswordPattern.ReplaceAllString(value, "${1}[已转入安全输入]${3}")
	value = uriPasswordPattern.ReplaceAllString(value, "${1}[已转入安全输入]${3}")
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for endpointIndex, line := range lines {
		if !positionalEndpointPattern.MatchString(line) {
			continue
		}
		following := followingNonEmptyLineIndexes(lines, endpointIndex, 2)
		if len(following) < 2 || !positionalUsernamePattern.MatchString(strings.TrimSpace(lines[following[0]])) {
			continue
		}
		candidate := strings.TrimSpace(lines[following[1]])
		if !looksLikePositionalPassword(candidate) {
			continue
		}
		lines[following[1]] = "[已转入安全输入]"
		mentioned = true
		break
	}
	return strings.Join(lines, "\n"), mentioned
}

func followingNonEmptyLineIndexes(lines []string, start, limit int) []int {
	result := make([]int, 0, limit)
	for index := start + 1; index < len(lines) && len(result) < limit; index++ {
		if strings.TrimSpace(lines[index]) != "" {
			result = append(result, index)
		}
	}
	return result
}

func looksLikePositionalPassword(value string) bool {
	if len(value) < 8 || len(value) > 512 || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return false
	}
	hasLetter, hasDigit, hasSymbol := false, false, false
	for _, character := range value {
		switch {
		case unicode.IsLetter(character):
			hasLetter = true
		case unicode.IsDigit(character):
			hasDigit = true
		default:
			hasSymbol = true
		}
	}
	return hasLetter && (hasDigit || hasSymbol)
}

func parseStructuredInstruction(instruction string, base Draft) parsedInstruction {
	result := parsedInstruction{Draft: base, Recognized: map[string]bool{}}
	seen := result.Recognized
	explicitType := structuredSourceType(instruction)
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
		case "database", "database_name", "databasename", "dbname",
			"database/service", "database/service_name", "databaseservice", "databaseservicename",
			"数据库", "数据库名称", "数据库/服务名", "数据库/服务名称", "库名":
			result.Draft.Database = value
			seen["database"] = true
		case "service", "service_name", "servicename", "oracle_service_name", "oracleservicename",
			"服务名", "服务名称", "oracle服务名", "oracle服务名称":
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
	if match := oracleJDBCThinPattern.FindStringSubmatch(instruction); len(match) == 4 {
		apply("type", string(datasource.TypeOracle))
		apply("host", match[1])
		apply("port", match[2])
		apply("service_name", match[3])
	}
	if match := sqlServerJDBCPattern.FindStringSubmatch(instruction); len(match) == 3 {
		apply("type", string(datasource.TypeSQLServer))
		apply("host", match[1])
		if match[2] == "" {
			apply("port", strconv.Itoa(datasource.DefaultDatabasePort(datasource.TypeSQLServer)))
		} else {
			apply("port", match[2])
		}
		if database := sqlServerDatabasePattern.FindStringSubmatch(match[0]); len(database) == 2 {
			apply("database", database[1])
		}
	}
	for _, raw := range connectionURIPattern.FindAllString(instruction, -1) {
		candidate := strings.TrimRight(raw, ",，。);；")
		candidate = strings.ReplaceAll(candidate, "[已转入安全输入]", "redacted")
		if strings.HasPrefix(strings.ToLower(candidate), "jdbc:") {
			candidate = candidate[len("jdbc:"):]
		}
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		sourceType := structuredSourceType(parsed.Scheme)
		if sourceType == "" {
			continue
		}
		apply("type", sourceType)
		apply("host", parsed.Hostname())
		port := parsed.Port()
		if port == "" {
			port = strconv.Itoa(datasource.DefaultDatabasePort(datasource.Type(sourceType)))
		}
		apply("port", port)
		database := strings.Trim(parsed.Path, "/")
		if database == "" {
			database = parsed.Query().Get("database")
		}
		if database == "" {
			database = parsed.Query().Get("databaseName")
		}
		if sourceType == string(datasource.TypeOracle) {
			apply("service_name", database)
		} else {
			apply("database", database)
		}
		if parsed.User != nil {
			apply("username", parsed.User.Username())
		}
	}
	lines := strings.Split(strings.ReplaceAll(instruction, "\r\n", "\n"), "\n")
	for endpointIndex, line := range lines {
		match := positionalEndpointPattern.FindStringSubmatch(line)
		if len(match) != 4 {
			continue
		}
		port, err := strconv.Atoi(match[2])
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		apply("host", match[1])
		apply("port", match[2])
		endpointType := explicitType
		if endpointType == "" {
			endpointType = string(datasource.DatabaseTypeFromPort(port))
		}
		if endpointType != "" {
			apply("type", endpointType)
			if endpointType == string(datasource.TypeOracle) {
				apply("service_name", match[3])
			} else {
				apply("database", match[3])
			}
		}
		following := followingNonEmptyLineIndexes(lines, endpointIndex, 1)
		if len(following) == 1 {
			username := strings.TrimSpace(lines[following[0]])
			if positionalUsernamePattern.MatchString(username) {
				apply("username", username)
			}
		}
		break
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
	if explicitType != "" {
		result.Draft.Type = explicitType
		seen["type"] = true
	}
	result.RecognizedFields = len(seen)
	return result
}

// applyRecognizedFields reapplies only values explicitly present in the current
// instruction. The parsed draft also contains the previous baseline, so merging
// the whole draft would incorrectly override unrelated fields returned by the
// model.
func applyRecognizedFields(value Draft, parsed parsedInstruction) Draft {
	if parsed.Recognized["type"] {
		value.Type = parsed.Draft.Type
	}
	if parsed.Recognized["host"] {
		value.Host = parsed.Draft.Host
	}
	if parsed.Recognized["port"] {
		value.Port = parsed.Draft.Port
	}
	if parsed.Recognized["database"] {
		value.Database = parsed.Draft.Database
	}
	if parsed.Recognized["oracleConnectMode"] {
		value.OracleConnectMode = parsed.Draft.OracleConnectMode
	}
	if parsed.Recognized["username"] {
		value.Username = parsed.Draft.Username
	}
	if parsed.Recognized["name"] {
		value.Name = parsed.Draft.Name
	}
	if parsed.Recognized["code"] {
		value.Code = parsed.Draft.Code
	}
	return value
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
	if strings.Contains(strings.ToLower(value), "excel") || strings.Contains(strings.ToLower(value), "csv") {
		return string(datasource.TypeExcel)
	}
	if value := datasource.DatabaseTypeFromText(value); value != "" {
		return string(value)
	}
	return ""
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
