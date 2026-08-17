package registry

import (
	_ "embed"
)

// SemanticBundleSchema 是 semantic-bundle/v1 导入合同的机器可读 JSON Schema，
// 通过 GET /semantic/imports/schema 发布给外部生成方与前端向导。
//
//go:embed schemas/semantic-bundle-v1.schema.json
var SemanticBundleSchema []byte
