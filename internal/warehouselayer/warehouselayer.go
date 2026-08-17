// Package warehouselayer 定义物理表资产上的“层级”标签合同。
//
// 元数据清洗（metadata AI）在完善一张表时必须判断它当前所处的数仓层级并写入
// 恰好一个 `层级:` 标签；之后人工可以在资产页修改。该标签只描述这张物理表
// 的既有粒度（见 docs/11_dataset-layer-contract.md），是系统为其生成默认
// 映射数据集时的首选层级；字段级资产不携带层级标签。
package warehouselayer

import "strings"

// TagPrefix 是受控标签的分面前缀；完整标签形如 "层级:DWS"。
const TagPrefix = "层级:"

// Layers 按数仓加工方向排列的五个层级编码。
var Layers = []string{"ODS", "DIM", "DWD", "DWS", "ADS"}

// Valid 判断层级编码是否合法。
func Valid(layer string) bool {
	for _, candidate := range Layers {
		if candidate == layer {
			return true
		}
	}
	return false
}

// Tag 返回层级对应的受控标签；非法层级返回空字符串。
func Tag(layer string) string {
	layer = strings.ToUpper(strings.TrimSpace(layer))
	if !Valid(layer) {
		return ""
	}
	return TagPrefix + layer
}

// Tags 返回全部合法的层级标签，顺序稳定。
func Tags() []string {
	tags := make([]string, 0, len(Layers))
	for _, layer := range Layers {
		tags = append(tags, TagPrefix+layer)
	}
	return tags
}

// IsTag 判断一个标签是否属于层级分面（无论其取值是否合法）。
func IsTag(tag string) bool {
	return strings.HasPrefix(strings.TrimSpace(tag), TagPrefix)
}

// FromTags 返回标签集合中声明的层级编码；没有合法层级标签时返回空字符串。
// 存在多个层级标签时返回第一个合法值，调用方应先用 Count 校验唯一性。
func FromTags(tags []string) string {
	for _, tag := range tags {
		if layer, ok := parse(tag); ok {
			return layer
		}
	}
	return ""
}

// Count 返回标签集合中层级分面标签的数量（含取值非法者），用于唯一性校验。
func Count(tags []string) int {
	count := 0
	for _, tag := range tags {
		if IsTag(tag) {
			count++
		}
	}
	return count
}

// Validate 校验一个目标的层级标签：表资产必须恰好一个合法层级标签
// （requireOne=true 时）或至多一个（requireOne=false 时）；字段资产不得携带。
func Validate(tags []string, column, requireOne bool) error {
	count := Count(tags)
	if column {
		if count > 0 {
			return errInvalid("字段资产不能携带层级标签")
		}
		return nil
	}
	if count > 1 {
		return errInvalid("表资产只能携带一个层级标签")
	}
	if count == 0 {
		if requireOne {
			return errInvalid("表资产必须携带一个层级标签（层级:ODS/DIM/DWD/DWS/ADS）")
		}
		return nil
	}
	if FromTags(tags) == "" {
		return errInvalid("层级标签取值必须是 ODS、DIM、DWD、DWS 或 ADS")
	}
	return nil
}

// Replace 返回把标签集合中的层级标签替换为给定层级后的新切片；layer 为空时只移除。
func Replace(tags []string, layer string) []string {
	out := make([]string, 0, len(tags)+1)
	for _, tag := range tags {
		if !IsTag(tag) {
			out = append(out, tag)
		}
	}
	if next := Tag(layer); next != "" {
		out = append(out, next)
	}
	return out
}

func parse(tag string) (string, bool) {
	tag = strings.TrimSpace(tag)
	if !strings.HasPrefix(tag, TagPrefix) {
		return "", false
	}
	layer := strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(tag, TagPrefix)))
	if !Valid(layer) {
		return "", false
	}
	return layer, true
}

type invalidError string

func (e invalidError) Error() string { return string(e) }

func errInvalid(message string) error { return invalidError(message) }
