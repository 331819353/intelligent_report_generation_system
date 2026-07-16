package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

var schemaNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var supportedSchemaKeywords = map[string]bool{
	"$comment": true, "$defs": true, "$ref": true,
	"additionalProperties": true, "allOf": true, "anyOf": true,
	"const": true, "definitions": true, "description": true, "enum": true,
	"exclusiveMaximum": true, "exclusiveMinimum": true,
	"items": true, "maxItems": true, "maxLength": true, "maxProperties": true,
	"maximum": true, "minItems": true, "minLength": true, "minProperties": true,
	"minimum": true, "multipleOf": true, "not": true, "oneOf": true,
	"pattern": true, "properties": true, "required": true, "title": true,
	"type": true, "uniqueItems": true,
}

var supportedSchemaTypes = map[string]bool{
	"array": true, "boolean": true, "integer": true, "null": true,
	"number": true, "object": true, "string": true,
}

const maxSchemaEvaluationSteps = 10_000

var errSchemaEvaluationLimit = errors.New("schema evaluation limit exceeded")

// ValidateProviderRequest 在发起网络请求前校验消息和严格输出合同。
func ValidateProviderRequest(request ProviderRequest) error {
	_, _, err := normalizeProviderRequest(request)
	return err
}

// ValidateStructuredOutput 校验单个 JSON 值并返回键顺序稳定的规范 JSON。
func ValidateStructuredOutput(schema JSONSchema, content []byte) (json.RawMessage, error) {
	_, root, err := normalizeJSONSchema(schema)
	if err != nil {
		return nil, err
	}
	return validateStructuredOutput(root, content)
}

// normalizeProviderRequest 深拷贝必要字段，并返回可直接发送的规范 Schema。
func normalizeProviderRequest(request ProviderRequest) (ProviderRequest, map[string]any, error) {
	if len(request.Messages) == 0 || len(request.Messages) > MaxMessagesPerRequest {
		return ProviderRequest{}, nil, invalidRequest(errors.New("messages count is invalid"))
	}
	hasUserMessage := false
	normalizedMessages := make([]Message, len(request.Messages))
	for i, message := range request.Messages {
		if message.Role != MessageRoleSystem && message.Role != MessageRoleUser && message.Role != MessageRoleAssistant {
			return ProviderRequest{}, nil, invalidRequest(fmt.Errorf("messages[%d] has invalid role", i))
		}
		if message.Role == MessageRoleUser {
			hasUserMessage = true
		}
		if len(message.Parts) == 0 || len(message.Parts) > MaxPartsPerMessage {
			return ProviderRequest{}, nil, invalidRequest(fmt.Errorf("messages[%d] parts count is invalid", i))
		}
		normalizedMessages[i] = Message{Role: message.Role, Parts: append([]ContentPart(nil), message.Parts...)}
		for j, part := range message.Parts {
			switch part.Type {
			case ContentTypeText:
				if !utf8.ValidString(part.Text) || strings.TrimSpace(part.Text) == "" || part.ImageURL != "" || part.ImageDetail != "" {
					return ProviderRequest{}, nil, invalidRequest(fmt.Errorf("messages[%d].parts[%d] is not valid text", i, j))
				}
			case ContentTypeImageURL:
				if message.Role != MessageRoleUser || part.Text != "" || !validImageDetail(part.ImageDetail) {
					return ProviderRequest{}, nil, invalidRequest(fmt.Errorf("messages[%d].parts[%d] is not a valid image", i, j))
				}
				if err := validateImageURL(part.ImageURL); err != nil {
					return ProviderRequest{}, nil, invalidRequest(fmt.Errorf("messages[%d].parts[%d]: %w", i, j, err))
				}
			default:
				return ProviderRequest{}, nil, invalidRequest(fmt.Errorf("messages[%d].parts[%d] has invalid type", i, j))
			}
		}
	}
	if !hasUserMessage {
		return ProviderRequest{}, nil, invalidRequest(errors.New("at least one user message is required"))
	}
	if request.Temperature != nil && (math.IsNaN(*request.Temperature) || math.IsInf(*request.Temperature, 0) || *request.Temperature < 0 || *request.Temperature > 2) {
		return ProviderRequest{}, nil, invalidRequest(errors.New("temperature is outside the supported range"))
	}
	if request.MaxOutputTokens < 0 || request.MaxOutputTokens > 1_000_000 {
		return ProviderRequest{}, nil, invalidRequest(errors.New("maxOutputTokens is outside the supported range"))
	}
	normalizedSchema, root, err := normalizeJSONSchema(request.ResponseSchema)
	if err != nil {
		return ProviderRequest{}, nil, err
	}
	request.Messages = normalizedMessages
	request.ResponseSchema = normalizedSchema
	return request, root, nil
}

// normalizeJSONSchema 确保 Schema 是单个严格对象合同，并生成规范 JSON。
func normalizeJSONSchema(schema JSONSchema) (JSONSchema, map[string]any, error) {
	if !schemaNamePattern.MatchString(schema.Name) {
		return JSONSchema{}, nil, invalidRequest(errors.New("response schema name is invalid"))
	}
	if !utf8.ValidString(schema.Description) || utf8.RuneCountInString(schema.Description) > 1024 {
		return JSONSchema{}, nil, invalidRequest(errors.New("response schema description is too long"))
	}
	value, err := decodeSingleJSONValue(schema.Schema)
	if err != nil {
		return JSONSchema{}, nil, invalidRequest(fmt.Errorf("response schema is invalid: %w", err))
	}
	root, ok := value.(map[string]any)
	if !ok {
		return JSONSchema{}, nil, invalidRequest(errors.New("response schema must be a JSON object"))
	}
	if rootType, ok := root["type"].(string); !ok || rootType != "object" {
		return JSONSchema{}, nil, invalidRequest(errors.New("response schema root type must be object"))
	}
	if err := validateStrictSchemaNode(root, root, "$schema"); err != nil {
		return JSONSchema{}, nil, invalidRequest(err)
	}
	canonical, err := json.Marshal(root)
	if err != nil {
		return JSONSchema{}, nil, invalidRequest(errors.New("response schema cannot be normalized"))
	}
	schema.Schema = canonical
	return schema, root, nil
}

// validateStrictSchemaNode 强制所有对象关闭额外字段并显式列出全部必填属性。
func validateStrictSchemaNode(node, root map[string]any, path string) error {
	return validateStrictSchemaNodeAtDepth(node, root, path, 0)
}

// validateStrictSchemaNodeAtDepth 限制 Schema 自身嵌套，避免恶意合同耗尽调用栈。
func validateStrictSchemaNodeAtDepth(node, root map[string]any, path string, depth int) error {
	if depth > 128 {
		return fmt.Errorf("%s exceeds schema nesting depth", path)
	}
	for keyword := range node {
		if !supportedSchemaKeywords[keyword] {
			return fmt.Errorf("%s contains unsupported keyword %q", path, keyword)
		}
	}
	typeValue, hasType := node["type"]
	if err := validateSchemaType(typeValue, hasType, path); err != nil {
		return err
	}
	for _, keyword := range []string{"title", "description", "$comment"} {
		if raw, exists := node[keyword]; exists {
			value, ok := raw.(string)
			if !ok || utf8.RuneCountInString(value) > 4096 {
				return fmt.Errorf("%s.%s must be a string of at most 4096 characters", path, keyword)
			}
		}
	}
	if refValue, exists := node["$ref"]; exists {
		ref, ok := refValue.(string)
		if !ok || !validLocalSchemaReference(ref) {
			return fmt.Errorf("%s contains an unsupported $ref", path)
		}
		if _, err := resolveLocalReference(root, ref); err != nil {
			return fmt.Errorf("%s contains an invalid $ref: %w", path, err)
		}
	}
	if path != "$schema" && hasAnySchemaKeyword(node, "$defs", "definitions") {
		return fmt.Errorf("%s contains nested definitions", path)
	}
	if patternValue, exists := node["pattern"]; exists {
		pattern, ok := patternValue.(string)
		if !ok {
			return fmt.Errorf("%s pattern must be a string", path)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("%s pattern is invalid", path)
		}
	}
	if enum, exists := node["enum"]; exists {
		values, ok := enum.([]any)
		if !ok || len(values) == 0 {
			return fmt.Errorf("%s enum must be a non-empty array", path)
		}
		seen := make(map[string]bool, len(values))
		for i, value := range values {
			if err := validateJSONLiteral(value, 0); err != nil {
				return fmt.Errorf("%s.enum[%d] is invalid: %w", path, i, err)
			}
			key, err := canonicalJSONEqualityKey(value)
			if err != nil {
				return fmt.Errorf("%s.enum[%d] is invalid: %w", path, i, err)
			}
			if seen[key] {
				return fmt.Errorf("%s enum contains duplicate values", path)
			}
			seen[key] = true
		}
	}
	if constant, exists := node["const"]; exists {
		if err := validateJSONLiteral(constant, 0); err != nil {
			return fmt.Errorf("%s const is invalid: %w", path, err)
		}
	}
	for _, pair := range [][2]string{{"minLength", "maxLength"}, {"minItems", "maxItems"}, {"minProperties", "maxProperties"}} {
		if err := validateSchemaCountBounds(node, pair[0], pair[1], path); err != nil {
			return err
		}
	}
	for _, keyword := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf"} {
		if raw, exists := node[keyword]; exists {
			number, ok := raw.(json.Number)
			if !ok {
				return fmt.Errorf("%s.%s must be a number", path, keyword)
			}
			value, valid := safeJSONRational(number)
			if !valid || (keyword == "multipleOf" && value.Sign() <= 0) {
				return fmt.Errorf("%s.%s is invalid", path, keyword)
			}
		}
	}
	if raw, exists := node["uniqueItems"]; exists {
		if _, ok := raw.(bool); !ok {
			return fmt.Errorf("%s.uniqueItems must be a boolean", path)
		}
	}

	objectKeywords := hasAnySchemaKeyword(node, "properties", "required", "additionalProperties", "minProperties", "maxProperties")
	if objectKeywords && !schemaIncludesType(node["type"], "object") {
		return fmt.Errorf("%s object keywords require object type", path)
	}
	if schemaIncludesType(node["type"], "object") {
		properties, ok := node["properties"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s object properties must be an object", path)
		}
		additional, exists := node["additionalProperties"]
		if !exists || additional != false {
			return fmt.Errorf("%s object must set additionalProperties to false", path)
		}
		requiredValues, ok := node["required"].([]any)
		if !ok {
			return fmt.Errorf("%s object must provide required", path)
		}
		required := make(map[string]bool, len(requiredValues))
		for _, value := range requiredValues {
			name, ok := value.(string)
			if !ok || required[name] {
				return fmt.Errorf("%s required contains an invalid or duplicate property", path)
			}
			required[name] = true
		}
		if len(required) != len(properties) {
			return fmt.Errorf("%s required must contain every property exactly once", path)
		}
		for name, child := range properties {
			if !required[name] {
				return fmt.Errorf("%s property %q is not required", path, name)
			}
			childNode, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("%s property %q must use an object schema", path, name)
			}
			if err := validateStrictSchemaNodeAtDepth(childNode, root, path+".properties."+name, depth+1); err != nil {
				return err
			}
		}
	}

	arrayKeywords := hasAnySchemaKeyword(node, "items", "minItems", "maxItems", "uniqueItems")
	if arrayKeywords && !schemaIncludesType(node["type"], "array") {
		return fmt.Errorf("%s array keywords require array type", path)
	}
	if schemaIncludesType(node["type"], "array") {
		child, exists := node["items"]
		if !exists {
			return fmt.Errorf("%s array must provide items", path)
		}
		childNode, ok := child.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.items must use an object schema", path)
		}
		if err := validateStrictSchemaNodeAtDepth(childNode, root, path+".items", depth+1); err != nil {
			return err
		}
	}
	if hasAnySchemaKeyword(node, "minLength", "maxLength", "pattern") && !schemaIncludesType(node["type"], "string") {
		return fmt.Errorf("%s string keywords require string type", path)
	}
	if hasAnySchemaKeyword(node, "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf") &&
		!schemaIncludesType(node["type"], "number") && !schemaIncludesType(node["type"], "integer") {
		return fmt.Errorf("%s numeric keywords require number or integer type", path)
	}

	for _, keyword := range []string{"not"} {
		if child, exists := node[keyword]; exists {
			childNode, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s must use an object schema", path, keyword)
			}
			if err := validateStrictSchemaNodeAtDepth(childNode, root, path+"."+keyword, depth+1); err != nil {
				return err
			}
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if children, exists := node[keyword]; exists {
			items, ok := children.([]any)
			if !ok || len(items) == 0 {
				return fmt.Errorf("%s.%s must be a non-empty schema array", path, keyword)
			}
			for i, child := range items {
				childNode, ok := child.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s[%d] must use an object schema", path, keyword, i)
				}
				if err := validateStrictSchemaNodeAtDepth(childNode, root, fmt.Sprintf("%s.%s[%d]", path, keyword, i), depth+1); err != nil {
					return err
				}
			}
		}
	}
	for _, keyword := range []string{"$defs", "definitions"} {
		if children, exists := node[keyword]; exists {
			items, ok := children.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s must be a schema object", path, keyword)
			}
			for name, child := range items {
				childNode, ok := child.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s.%s must use an object schema", path, keyword, name)
				}
				if err := validateStrictSchemaNodeAtDepth(childNode, root, path+"."+keyword+"."+name, depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validateSchemaType 校验基础类型声明，避免无效类型直到模型返回后才暴露。
func validateSchemaType(raw any, exists bool, path string) error {
	if !exists {
		return nil
	}
	if value, ok := raw.(string); ok {
		if supportedSchemaTypes[value] {
			return nil
		}
		return fmt.Errorf("%s.type contains unsupported type %q", path, value)
	}
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return fmt.Errorf("%s.type must be a type name or non-empty type array", path)
	}
	seen := make(map[string]bool, len(values))
	for _, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok || !supportedSchemaTypes[value] || seen[value] {
			return fmt.Errorf("%s.type contains an invalid or duplicate type", path)
		}
		seen[value] = true
	}
	return nil
}

// validateSchemaCountBounds 校验长度、数量和属性个数边界必须是非负整数。
func validateSchemaCountBounds(node map[string]any, minimumKeyword, maximumKeyword, path string) error {
	minimumRaw, hasMinimum := node[minimumKeyword]
	minimum := int64(0)
	if hasMinimum {
		var err error
		minimum, err = schemaNonNegativeInteger(minimumRaw)
		if err != nil {
			return fmt.Errorf("%s.%s must be a non-negative integer", path, minimumKeyword)
		}
	}
	maximumRaw, hasMaximum := node[maximumKeyword]
	maximum := int64(0)
	if hasMaximum {
		var err error
		maximum, err = schemaNonNegativeInteger(maximumRaw)
		if err != nil {
			return fmt.Errorf("%s.%s must be a non-negative integer", path, maximumKeyword)
		}
	}
	if hasMinimum && hasMaximum && minimum > maximum {
		return fmt.Errorf("%s.%s cannot exceed %s", path, minimumKeyword, maximumKeyword)
	}
	return nil
}

func schemaNonNegativeInteger(raw any) (int64, error) {
	number, ok := raw.(json.Number)
	if !ok {
		return 0, errors.New("not a number")
	}
	value, valid := safeJSONRational(number)
	if !valid || value.Sign() < 0 || value.Denom().Cmp(big.NewInt(1)) != 0 || !value.Num().IsInt64() {
		return 0, errors.New("not a non-negative integer")
	}
	return value.Num().Int64(), nil
}

func hasAnySchemaKeyword(node map[string]any, keywords ...string) bool {
	for _, keyword := range keywords {
		if _, exists := node[keyword]; exists {
			return true
		}
	}
	return false
}

// validLocalSchemaReference 仅允许引用根级命名定义，避免把常量对象误当成 Schema。
func validLocalSchemaReference(ref string) bool {
	segments := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	if !strings.HasPrefix(ref, "#/") || len(segments) != 2 ||
		(segments[0] != "$defs" && segments[0] != "definitions") || segments[1] == "" {
		return false
	}
	for index := 0; index < len(segments[1]); index++ {
		if segments[1][index] != '~' {
			continue
		}
		if index+1 >= len(segments[1]) || (segments[1][index+1] != '0' && segments[1][index+1] != '1') {
			return false
		}
		index++
	}
	return true
}

// validateJSONLiteral 限制 Schema 常量中的递归深度和数值规模。
func validateJSONLiteral(value any, depth int) error {
	if depth > 128 {
		return errors.New("JSON literal exceeds nesting depth")
	}
	switch typed := value.(type) {
	case json.Number:
		if _, ok := safeJSONRational(typed); !ok {
			return errors.New("JSON literal contains an invalid number")
		}
	case []any:
		for _, child := range typed {
			if err := validateJSONLiteral(child, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, child := range typed {
			if err := validateJSONLiteral(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateStructuredOutput 拒绝重复键、尾随 JSON 和不符合合同的值。
func validateStructuredOutput(schemaRoot map[string]any, content []byte) (json.RawMessage, error) {
	value, err := decodeSingleJSONValue(content)
	if err != nil {
		return nil, invalidOutput(err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, invalidOutput(errors.New("structured output must be a JSON object"))
	}
	if err := validateJSONValue(schemaRoot, schemaRoot, value, "$"); err != nil {
		return nil, invalidOutput(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, invalidOutput(errors.New("structured output cannot be normalized"))
	}
	return canonical, nil
}

// validateJSONValue 校验严格合同中常用的对象、数组、枚举和数值约束。
func validateJSONValue(schema, root map[string]any, value any, path string) error {
	remaining := maxSchemaEvaluationSteps
	return validateJSONValueAtDepth(schema, root, value, path, 0, &remaining)
}

// validateJSONValueAtDepth 同时限制递归深度和总求值步数，阻断组合引用指数展开。
func validateJSONValueAtDepth(schema, root map[string]any, value any, path string, depth int, remaining *int) error {
	if depth > 128 || remaining == nil || *remaining <= 0 {
		return fmt.Errorf("%w at %s", errSchemaEvaluationLimit, path)
	}
	*remaining = *remaining - 1
	if ref, ok := schema["$ref"].(string); ok {
		resolved, err := resolveLocalReference(root, ref)
		if err != nil {
			return fmt.Errorf("%s cannot resolve schema reference", path)
		}
		if err := validateJSONValueAtDepth(resolved, root, value, path, depth+1, remaining); err != nil {
			return err
		}
	}
	if constant, exists := schema["const"]; exists && !jsonValuesEqual(constant, value) {
		return fmt.Errorf("%s does not match const", path)
	}
	if enumValue, exists := schema["enum"]; exists {
		candidates, ok := enumValue.([]any)
		if !ok {
			return fmt.Errorf("%s contains an invalid enum schema", path)
		}
		matched := false
		for _, candidate := range candidates {
			if jsonValuesEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is outside enum", path)
		}
	}
	if err := validateComposedSchemas(schema, root, value, path, depth, remaining); err != nil {
		return err
	}
	if schemaType, exists := schema["type"]; exists && !matchesSchemaType(schemaType, value) {
		return fmt.Errorf("%s has an invalid type", path)
	}

	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, rawName := range required {
				name, ok := rawName.(string)
				if !ok {
					return fmt.Errorf("%s contains an invalid required schema", path)
				}
				if _, exists := typed[name]; !exists {
					return fmt.Errorf("%s is missing required property %q", path, name)
				}
			}
		}
		for name, childValue := range typed {
			childSchema, exists := properties[name]
			if !exists {
				if schema["additionalProperties"] == false {
					return fmt.Errorf("%s contains unknown property %q", path, name)
				}
				continue
			}
			childNode, ok := childSchema.(map[string]any)
			if !ok {
				return fmt.Errorf("%s contains an invalid property schema", path)
			}
			if err := validateJSONValueAtDepth(childNode, root, childValue, path+"."+name, depth+1, remaining); err != nil {
				return err
			}
		}
		if err := validateCountKeywords(schema, len(typed), "Properties", path); err != nil {
			return err
		}
	case []any:
		if err := validateCountKeywords(schema, len(typed), "Items", path); err != nil {
			return err
		}
		if schema["uniqueItems"] == true {
			seen := make(map[string]bool, len(typed))
			for _, item := range typed {
				key, err := canonicalJSONEqualityKey(item)
				if err != nil {
					return fmt.Errorf("%s contains an invalid item: %w", path, err)
				}
				if seen[key] {
					return fmt.Errorf("%s contains duplicate items", path)
				}
				seen[key] = true
			}
		}
		if rawItems, exists := schema["items"]; exists {
			itemSchema, ok := rawItems.(map[string]any)
			if !ok {
				return fmt.Errorf("%s contains an invalid items schema", path)
			}
			for i, item := range typed {
				if err := validateJSONValueAtDepth(itemSchema, root, item, fmt.Sprintf("%s[%d]", path, i), depth+1, remaining); err != nil {
					return err
				}
			}
		}
	case string:
		length := utf8.RuneCountInString(typed)
		if err := validateCountKeywords(schema, length, "Length", path); err != nil {
			return err
		}
		if pattern, ok := schema["pattern"].(string); ok && !regexp.MustCompile(pattern).MatchString(typed) {
			return fmt.Errorf("%s does not match pattern", path)
		}
	case json.Number:
		if err := validateNumberKeywords(schema, typed, path); err != nil {
			return err
		}
	}
	return nil
}

// validateComposedSchemas 实现 allOf、anyOf、oneOf 和 not 的确定性校验。
func validateComposedSchemas(schema, root map[string]any, value any, path string, depth int, remaining *int) error {
	if rawChildren, exists := schema["allOf"]; exists {
		children, ok := rawChildren.([]any)
		if !ok {
			return fmt.Errorf("%s contains an invalid allOf schema", path)
		}
		for _, child := range children {
			childNode, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("%s contains an invalid allOf child", path)
			}
			if err := validateJSONValueAtDepth(childNode, root, value, path, depth+1, remaining); err != nil {
				return err
			}
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if rawChildren, exists := schema[keyword]; exists {
			children, ok := rawChildren.([]any)
			if !ok {
				return fmt.Errorf("%s contains an invalid %s schema", path, keyword)
			}
			matches := 0
			for _, child := range children {
				childNode, ok := child.(map[string]any)
				if !ok {
					return fmt.Errorf("%s contains an invalid %s child", path, keyword)
				}
				err := validateJSONValueAtDepth(childNode, root, value, path, depth+1, remaining)
				if errors.Is(err, errSchemaEvaluationLimit) {
					return err
				}
				if err == nil {
					matches++
					if keyword == "anyOf" {
						break
					}
				}
			}
			if (keyword == "anyOf" && matches == 0) || (keyword == "oneOf" && matches != 1) {
				return fmt.Errorf("%s does not satisfy %s", path, keyword)
			}
		}
	}
	if rawChild, exists := schema["not"]; exists {
		child, ok := rawChild.(map[string]any)
		if !ok {
			return fmt.Errorf("%s contains an invalid not schema", path)
		}
		err := validateJSONValueAtDepth(child, root, value, path, depth+1, remaining)
		if errors.Is(err, errSchemaEvaluationLimit) {
			return err
		}
		if err == nil {
			return fmt.Errorf("%s matches a forbidden schema", path)
		}
	}
	return nil
}

// validateCountKeywords 校验字符串、数组和对象共享的数量边界。
func validateCountKeywords(schema map[string]any, count int, suffix, path string) error {
	if raw, exists := schema["min"+suffix]; exists {
		value, err := schemaNonNegativeInteger(raw)
		if err != nil || int64(count) < value {
			return fmt.Errorf("%s is below minimum size", path)
		}
	}
	if raw, exists := schema["max"+suffix]; exists {
		value, err := schemaNonNegativeInteger(raw)
		if err != nil || int64(count) > value {
			return fmt.Errorf("%s exceeds maximum size", path)
		}
	}
	return nil
}

// validateNumberKeywords 使用有理数比较，避免浮点舍入改变 Schema 语义。
func validateNumberKeywords(schema map[string]any, number json.Number, path string) error {
	value, ok := safeJSONRational(number)
	if !ok {
		return fmt.Errorf("%s contains an invalid number", path)
	}
	for keyword, comparison := range map[string]int{"minimum": -1, "maximum": 1} {
		if boundary, ok := schema[keyword].(json.Number); ok {
			limit, valid := safeJSONRational(boundary)
			if !valid || (comparison < 0 && value.Cmp(limit) < 0) || (comparison > 0 && value.Cmp(limit) > 0) {
				return fmt.Errorf("%s violates %s", path, keyword)
			}
		}
	}
	for keyword, comparison := range map[string]int{"exclusiveMinimum": -1, "exclusiveMaximum": 1} {
		if boundary, ok := schema[keyword].(json.Number); ok {
			limit, valid := safeJSONRational(boundary)
			if !valid || (comparison < 0 && value.Cmp(limit) <= 0) || (comparison > 0 && value.Cmp(limit) >= 0) {
				return fmt.Errorf("%s violates %s", path, keyword)
			}
		}
	}
	if multiple, ok := schema["multipleOf"].(json.Number); ok {
		factor, valid := safeJSONRational(multiple)
		if !valid || factor.Sign() <= 0 || new(big.Rat).Quo(value, factor).Denom().Cmp(big.NewInt(1)) != 0 {
			return fmt.Errorf("%s violates multipleOf", path)
		}
	}
	return nil
}

// resolveLocalReference 仅解析当前 Schema 内的 JSON Pointer，禁止网络引用。
func resolveLocalReference(root map[string]any, ref string) (map[string]any, error) {
	current := any(root)
	for _, rawSegment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		segment := strings.ReplaceAll(strings.ReplaceAll(rawSegment, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, errors.New("reference traverses a non-object value")
		}
		current, ok = object[segment]
		if !ok {
			return nil, errors.New("reference target does not exist")
		}
	}
	resolved, ok := current.(map[string]any)
	if !ok {
		return nil, errors.New("reference target is not a schema object")
	}
	return resolved, nil
}

// matchesSchemaType 判断规范 JSON 值是否匹配单类型或联合类型声明。
func matchesSchemaType(schemaType any, value any) bool {
	switch typed := schemaType.(type) {
	case string:
		return matchesSingleType(typed, value)
	case []any:
		for _, item := range typed {
			name, ok := item.(string)
			if ok && matchesSingleType(name, value) {
				return true
			}
		}
	}
	return false
}

// matchesSingleType 判断一个 JSON 值的基础类型。
func matchesSingleType(schemaType string, value any) bool {
	switch schemaType {
	case "null":
		return value == nil
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		rational, valid := safeJSONRational(number)
		return valid && rational.Denom().Cmp(big.NewInt(1)) == 0
	default:
		return false
	}
}

// schemaIncludesType 判断 Schema 类型声明是否包含指定类型。
func schemaIncludesType(schemaType any, target string) bool {
	switch typed := schemaType.(type) {
	case string:
		return typed == target
	case []any:
		for _, item := range typed {
			if item == target {
				return true
			}
		}
	}
	return false
}

// jsonValuesEqual 递归按 JSON Schema 语义比较，数值不受小数文本写法影响。
func jsonValuesEqual(left, right any) bool {
	switch leftValue := left.(type) {
	case json.Number:
		rightValue, ok := right.(json.Number)
		if !ok {
			return false
		}
		leftRat, leftValid := safeJSONRational(leftValue)
		rightRat, rightValid := safeJSONRational(rightValue)
		return leftValid && rightValid && leftRat.Cmp(rightRat) == 0
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for i := range leftValue {
			if !jsonValuesEqual(leftValue[i], rightValue[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for key, value := range leftValue {
			rightChild, exists := rightValue[key]
			if !exists || !jsonValuesEqual(value, rightChild) {
				return false
			}
		}
		return true
	default:
		return left == right
	}
}

// canonicalJSONEqualityKey 为 uniqueItems 生成带类型边界的稳定比较键。
func canonicalJSONEqualityKey(value any) (string, error) {
	var builder strings.Builder
	if err := appendJSONEqualityKey(&builder, value, 0); err != nil {
		return "", err
	}
	return builder.String(), nil
}

func appendJSONEqualityKey(builder *strings.Builder, value any, depth int) error {
	if depth > 128 {
		return errors.New("JSON value exceeds equality depth")
	}
	switch typed := value.(type) {
	case nil:
		builder.WriteString("n;")
	case bool:
		if typed {
			builder.WriteString("b1;")
		} else {
			builder.WriteString("b0;")
		}
	case string:
		builder.WriteByte('s')
		builder.WriteString(strconv.Itoa(len(typed)))
		builder.WriteByte(':')
		builder.WriteString(typed)
	case json.Number:
		value, valid := safeJSONRational(typed)
		if !valid {
			return errors.New("JSON value contains an invalid number")
		}
		number := value.RatString()
		builder.WriteByte('d')
		builder.WriteString(strconv.Itoa(len(number)))
		builder.WriteByte(':')
		builder.WriteString(number)
	case []any:
		builder.WriteByte('a')
		builder.WriteString(strconv.Itoa(len(typed)))
		builder.WriteByte('[')
		for _, child := range typed {
			if err := appendJSONEqualityKey(builder, child, depth+1); err != nil {
				return err
			}
		}
		builder.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('o')
		builder.WriteString(strconv.Itoa(len(keys)))
		builder.WriteByte('{')
		for _, key := range keys {
			builder.WriteString(strconv.Itoa(len(key)))
			builder.WriteByte(':')
			builder.WriteString(key)
			if err := appendJSONEqualityKey(builder, typed[key], depth+1); err != nil {
				return err
			}
		}
		builder.WriteByte('}')
	default:
		return errors.New("JSON value contains an unsupported type")
	}
	return nil
}

// decodeSingleJSONValue 拒绝重复对象键、空输入和尾随第二个 JSON 值。
func decodeSingleJSONValue(raw []byte) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("JSON value is empty")
	}
	if !utf8.Valid(raw) {
		return nil, errors.New("JSON contains invalid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("JSON contains a trailing value")
		}
		return nil, err
	}
	return value, nil
}

// safeJSONRational 限制指数和字面量长度，避免极端数字触发无界大整数分配。
func safeJSONRational(number json.Number) (*big.Rat, bool) {
	value := number.String()
	if len(value) == 0 || len(value) > 128 {
		return nil, false
	}
	if index := strings.IndexAny(value, "eE"); index >= 0 {
		exponent, err := strconv.ParseInt(value[index+1:], 10, 32)
		if err != nil || exponent < -10_000 || exponent > 10_000 {
			return nil, false
		}
	}
	rational, ok := new(big.Rat).SetString(value)
	return rational, ok
}

// rejectDuplicateJSONKeys 递归拒绝同一对象中的重复键，避免规范化前后语义漂移。
func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeJSONTokenValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); err == nil {
		return fmt.Errorf("JSON contains trailing token %v", token)
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// consumeJSONTokenValue 消费一个完整 JSON 值并跟踪当前对象的键集合。
func consumeJSONTokenValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("JSON object contains duplicate key %q", key)
			}
			seen[key] = true
			if err := consumeJSONTokenValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONTokenValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("JSON contains an unexpected delimiter")
	}
	return nil
}

// validateImageURL 只允许不含用户信息的 HTTPS 远程图片。
func validateImageURL(value string) error {
	if !utf8.ValidString(value) || value == "" || value != strings.TrimSpace(value) {
		return errors.New("image URL is empty or contains surrounding whitespace")
	}
	parsed, err := url.Parse(value)
	if err != nil || len(value) > 4096 || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return errors.New("image URL must use HTTPS without user information")
	}
	return nil
}

// validImageDetail 校验兼容协议支持的图片精度枚举。
func validImageDetail(detail ImageDetail) bool {
	return detail == "" || detail == ImageDetailAuto || detail == ImageDetailLow || detail == ImageDetailHigh
}

// invalidRequest 构造不会泄露调用内容的请求错误。
func invalidRequest(cause error) *ProviderError {
	return newProviderError(ErrorCodeInvalidRequest, "AI request is invalid", 0, false, 0, cause)
}

// invalidOutput 构造不会回显模型正文的结构化输出错误。
func invalidOutput(cause error) *ProviderError {
	return newProviderError(ErrorCodeInvalidOutput, "AI structured output is invalid", 0, false, 0, cause)
}
