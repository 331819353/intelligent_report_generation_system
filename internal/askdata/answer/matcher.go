package answer

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/shared"
)

const ResultEvidenceVersion = "answer-result-evidence-v1"

type ValueKind string

const (
	ValueNumber          ValueKind = "NUMBER"
	ValueRatio           ValueKind = "RATIO"
	ValuePercentagePoint ValueKind = "PERCENTAGE_POINT"
)

type ResultCell struct {
	Ref              shared.CellRef `json:"ref"`
	MetricVersionID  askdata.ID     `json:"metricVersionId"`
	Value            string         `json:"value"`
	ValueKind        ValueKind      `json:"valueKind"`
	Unit             string         `json:"unit"`
	Currency         string         `json:"currency"`
	DisplayPrecision int            `json:"displayPrecision"`
}

type DerivationEvidence struct {
	ID           askdata.ID       `json:"id"`
	Left         shared.CellRef   `json:"left"`
	Right        shared.CellRef   `json:"right"`
	AllowedRules []DerivationName `json:"allowedRules"`
}

type ResultEvidence struct {
	Version       string               `json:"version"`
	ReferenceHash askdata.ContentHash  `json:"referenceHash"`
	Cells         []ResultCell         `json:"cells"`
	Derivations   []DerivationEvidence `json:"derivations"`
}

func (evidence ResultEvidence) Normalize() ResultEvidence {
	result := evidence
	result.Cells = append([]ResultCell(nil), evidence.Cells...)
	result.Derivations = append([]DerivationEvidence(nil), evidence.Derivations...)
	for index := range result.Derivations {
		result.Derivations[index].AllowedRules = append([]DerivationName(nil), result.Derivations[index].AllowedRules...)
		sort.SliceStable(result.Derivations[index].AllowedRules, func(left, right int) bool {
			return result.Derivations[index].AllowedRules[left] < result.Derivations[index].AllowedRules[right]
		})
	}
	sort.SliceStable(result.Cells, func(left, right int) bool { return refKey(result.Cells[left].Ref) < refKey(result.Cells[right].Ref) })
	sort.SliceStable(result.Derivations, func(left, right int) bool { return result.Derivations[left].ID < result.Derivations[right].ID })
	if result.Cells == nil {
		result.Cells = []ResultCell{}
	}
	if result.Derivations == nil {
		result.Derivations = []DerivationEvidence{}
	}
	return result
}

func (evidence ResultEvidence) Validate() error {
	if evidence.Version != ResultEvidenceVersion || evidence.ReferenceHash.Validate() != nil ||
		len(evidence.Cells) == 0 || len(evidence.Cells) > 20000 || len(evidence.Derivations) > 256 {
		return errors.New("result evidence must contain bounded cells")
	}
	seen := make(map[string]struct{}, len(evidence.Cells))
	for index, cell := range evidence.Cells {
		if err := cell.Ref.Validate(); err != nil {
			return fmt.Errorf("cells[%d]: %w", index, err)
		}
		if cell.MetricVersionID.Validate() != nil || cell.DisplayPrecision < 0 || cell.DisplayPrecision > 12 {
			return fmt.Errorf("cells[%d]: invalid metric contract", index)
		}
		if _, ok := parseDecimal(cell.Value); !ok {
			return fmt.Errorf("cells[%d]: value is not an exact decimal", index)
		}
		kind := normalizedCellKind(cell)
		if kind != NumericScalar && kind != NumericPercent && kind != NumericPercentagePoint {
			return fmt.Errorf("cells[%d]: invalid numeric kind", index)
		}
		key := refKey(cell.Ref)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("cells[%d]: duplicate address", index)
		}
		seen[key] = struct{}{}
	}
	rules := make(map[DerivationName]struct{})
	for _, rule := range fixedDerivationRules() {
		rules[rule.Name] = struct{}{}
	}
	for index, derivation := range evidence.Derivations {
		if derivation.ID.Validate() != nil || derivation.Left.Validate() != nil || derivation.Right.Validate() != nil ||
			len(derivation.AllowedRules) == 0 || len(derivation.AllowedRules) > len(rules) {
			return fmt.Errorf("derivations[%d]: invalid declaration", index)
		}
		if _, exists := seen[refKey(derivation.Left)]; !exists {
			return fmt.Errorf("derivations[%d]: left source is absent", index)
		}
		if _, exists := seen[refKey(derivation.Right)]; !exists {
			return fmt.Errorf("derivations[%d]: right source is absent", index)
		}
		seenRules := map[DerivationName]bool{}
		for _, allowed := range derivation.AllowedRules {
			if _, exists := rules[allowed]; !exists || seenRules[allowed] {
				return fmt.Errorf("derivations[%d]: invalid allowed rule", index)
			}
			seenRules[allowed] = true
		}
	}
	return nil
}

func (binding BindingEvidence) Validate() error {
	if binding.Version != BindingEvidenceVersion || len(binding.Objects) > 512 {
		return errors.New("invalid binding evidence header")
	}
	// Exactly one catalog identity, matching the declared source. An unset or
	// mismatched identity would leave object names unanchored, which is the
	// hallucination check this evidence exists to support.
	switch binding.Source {
	case BindingSourceSemanticRelease:
		if binding.SemanticReleaseID.Validate() != nil || binding.DatasetVersionID != "" {
			return errors.New("SEMANTIC_RELEASE binding requires only a valid semanticReleaseId")
		}
	case BindingSourceDatasetVersion:
		if binding.DatasetVersionID.Validate() != nil || binding.SemanticReleaseID != "" {
			return errors.New("DATASET_VERSION binding requires only a valid datasetVersionId")
		}
	default:
		return errors.New("binding evidence source is invalid")
	}
	seenNames := map[string]bool{}
	seenObjects := map[askdata.ID]bool{}
	for index, object := range binding.Objects {
		if object.Kind != ObjectMetric && object.Kind != ObjectDimension && object.Kind != ObjectMember ||
			object.ObjectID.Validate() != nil || seenObjects[object.ObjectID] {
			return fmt.Errorf("objects[%d]: invalid identity", index)
		}
		seenObjects[object.ObjectID] = true
		if len(object.Names) == 0 || len(object.Names) > 32 {
			return fmt.Errorf("objects[%d]: names are required", index)
		}
		for _, name := range object.Names {
			canonical := strings.TrimSpace(name)
			if canonical == "" || !utf8.ValidString(canonical) || utf8.RuneCountInString(canonical) > 128 || seenNames[canonical] {
				return fmt.Errorf("objects[%d]: invalid or duplicate name", index)
			}
			seenNames[canonical] = true
		}
	}
	return nil
}

type evidenceIndex struct {
	byRef map[string]ResultCell
	rules map[DerivationName]DerivationRule
}

func newEvidenceIndex(evidence ResultEvidence) evidenceIndex {
	index := evidenceIndex{
		byRef: make(map[string]ResultCell, len(evidence.Cells)), rules: make(map[DerivationName]DerivationRule),
	}
	for _, cell := range evidence.Cells {
		index.byRef[refKey(cell.Ref)] = cell
	}
	for _, rule := range fixedDerivationRules() {
		index.rules[rule.Name] = rule
	}
	return index
}

func normalizedCellKind(cell ResultCell) NumericKind {
	switch cell.ValueKind {
	case ValueNumber:
		return NumericScalar
	case ValueRatio:
		return NumericPercent
	case ValuePercentagePoint:
		return NumericPercentagePoint
	}
	if canonicalUnit(cell.Unit) == "PERCENT" {
		return NumericPercent
	}
	return NumericScalar
}

func refKey(ref shared.CellRef) string { return ref.RowKey + "\x00" + ref.ColumnKey }

func (index evidenceIndex) matchNumber(element ExtractedElement, citation shared.Citation, evidence ResultEvidence) (bool, []string) {
	ref, ok := citation.CellRef()
	if !ok {
		return false, []string{"RESULT_CELL citation"}
	}
	want, ok := parseDecimal(element.Value)
	if !ok {
		return false, []string{"canonical numeric value"}
	}
	expected := make([]string, 0)
	if cell, exists := index.byRef[refKey(ref)]; exists {
		cellValue, _ := parseDecimal(cell.Value)
		kind := normalizedCellKind(cell)
		if kind == element.NumericKind && withinTolerance(want, cellValue, cell.DisplayPrecision) {
			return true, nil
		}
		expected = append(expected, formatExpectedNumber(cell.Value, kind, cell.DisplayPrecision))
	}
	for _, declaration := range evidence.Derivations {
		if declaration.Left != ref {
			continue
		}
		left, leftOK := index.byRef[refKey(declaration.Left)]
		right, rightOK := index.byRef[refKey(declaration.Right)]
		if !leftOK || !rightOK {
			continue
		}
		leftValue, _ := parseDecimal(left.Value)
		rightValue, _ := parseDecimal(right.Value)
		precision := left.DisplayPrecision
		for _, allowed := range declaration.AllowedRules {
			rule, ruleOK := index.rules[allowed]
			if !ruleOK {
				continue
			}
			derived, err := rule.apply(leftValue, rightValue)
			if err != nil {
				continue
			}
			kind := derivationOutputKind(allowed, normalizedCellKind(left), normalizedCellKind(right))
			if kind == element.NumericKind && withinTolerance(want, derived, precision) {
				return true, nil
			}
			expected = append(expected, formatExpectedRat(derived, kind, precision))
		}
	}
	sort.Strings(expected)
	return false, uniqueStrings(expected)
}

func (index evidenceIndex) matchUnit(element ExtractedElement, citation shared.Citation) (bool, []string) {
	want := canonicalUnit(element.Unit)
	expected := make([]string, 0)
	cells := make([]ResultCell, 0)
	if ref, ok := citation.CellRef(); ok {
		if cell, exists := index.byRef[refKey(ref)]; exists {
			cells = append(cells, cell)
		}
	} else if citation.Kind == shared.CitationContract && citation.ContractID != nil {
		for _, cell := range index.byRef {
			if cell.MetricVersionID == *citation.ContractID {
				cells = append(cells, cell)
			}
		}
	}
	if len(cells) == 0 {
		return false, []string{"RESULT_CELL or metric CONTRACT citation"}
	}
	for _, cell := range cells {
		units := []string{canonicalUnit(cell.Unit), canonicalUnit(cell.Currency)}
		for _, unit := range units {
			if unit != "" {
				expected = append(expected, unit)
			}
			if want != "" && unit == want {
				return true, nil
			}
		}
	}
	sort.Strings(expected)
	return false, uniqueStrings(expected)
}

func withinTolerance(actual, expected *big.Rat, precision int) bool {
	difference := new(big.Rat).Sub(actual, expected)
	if difference.Sign() < 0 {
		difference.Neg(difference)
	}
	power := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(precision)), nil)
	tolerance := new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Mul(big.NewInt(2), power))
	return difference.Cmp(tolerance) <= 0
}

func formatExpectedNumber(value string, kind NumericKind, precision int) string {
	parsed, _ := parseDecimal(value)
	return formatExpectedRat(parsed, kind, precision)
}

func formatExpectedRat(value *big.Rat, kind NumericKind, precision int) string {
	if value == nil {
		return ""
	}
	if kind == NumericPercent || kind == NumericPercentagePoint {
		return new(big.Rat).Mul(value, big.NewRat(100, 1)).FloatString(precision) + map[bool]string{true: "个百分点", false: "%"}[kind == NumericPercentagePoint]
	}
	return value.FloatString(precision)
}

func parseDecimal(value string) (*big.Rat, bool) {
	if !decimalPattern.MatchString(value) {
		return nil, false
	}
	result, ok := new(big.Rat).SetString(value)
	return result, ok
}

func normalizeNumericText(raw string) (string, NumericKind, string, bool) {
	value := strings.TrimSpace(raw)
	kind := NumericScalar
	unit := ""
	scale := big.NewRat(1, 1)
	if strings.HasPrefix(value, "百分之") {
		parsed, ok := parseChineseNumber(strings.TrimPrefix(value, "百分之"))
		if !ok {
			return "", "", "", false
		}
		parsed.Quo(parsed, big.NewRat(100, 1))
		return finiteDecimal(parsed), NumericPercent, "PERCENT", true
	}
	for _, suffix := range []string{"个百分点", "个点", "pp", "PP", "%", "％", "万", "亿"} {
		if !strings.HasSuffix(value, suffix) {
			continue
		}
		value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
		switch suffix {
		case "个百分点", "个点", "pp", "PP":
			kind, unit, scale = NumericPercentagePoint, "PERCENTAGE_POINT", big.NewRat(1, 100)
		case "%", "％":
			kind, unit, scale = NumericPercent, "PERCENT", big.NewRat(1, 100)
		case "万":
			scale = big.NewRat(10_000, 1)
		case "亿":
			scale = big.NewRat(100_000_000, 1)
		}
		break
	}
	var parsed *big.Rat
	var ok bool
	if regexp.MustCompile(`^[零〇一二两三四五六七八九十百千万亿点]+$`).MatchString(value) {
		parsed, ok = parseChineseNumber(value)
	} else {
		value = strings.ReplaceAll(value, ",", "")
		parsed, ok = new(big.Rat).SetString(value)
	}
	if !ok {
		return "", "", "", false
	}
	parsed.Mul(parsed, scale)
	return finiteDecimal(parsed), kind, unit, true
}

func parseChineseNumber(value string) (*big.Rat, bool) {
	parts := strings.Split(value, "点")
	if len(parts) > 2 || parts[0] == "" {
		return nil, false
	}
	digits := map[rune]int64{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	units := map[rune]int64{'十': 10, '百': 100, '千': 1000, '万': 10_000, '亿': 100_000_000}
	var total, section, number int64
	for _, character := range parts[0] {
		if digit, exists := digits[character]; exists {
			number = digit
			continue
		}
		unit, exists := units[character]
		if !exists {
			return nil, false
		}
		if unit < 10_000 {
			if number == 0 {
				number = 1
			}
			section += number * unit
		} else {
			section += number
			if section == 0 {
				section = 1
			}
			total += section * unit
			section = 0
		}
		number = 0
	}
	result := big.NewRat(total+section+number, 1)
	if len(parts) == 2 {
		if parts[1] == "" {
			return nil, false
		}
		denominator := int64(1)
		fraction := int64(0)
		for _, character := range parts[1] {
			digit, exists := digits[character]
			if !exists {
				return nil, false
			}
			fraction = fraction*10 + digit
			denominator *= 10
		}
		result.Add(result, big.NewRat(fraction, denominator))
	}
	return result, true
}

func finiteDecimal(value *big.Rat) string {
	denominator := new(big.Int).Set(value.Denom())
	twos, fives := 0, 0
	two, five := big.NewInt(2), big.NewInt(5)
	zero := big.NewInt(0)
	for new(big.Int).Mod(denominator, two).Cmp(zero) == 0 {
		denominator.Div(denominator, two)
		twos++
	}
	for new(big.Int).Mod(denominator, five).Cmp(zero) == 0 {
		denominator.Div(denominator, five)
		fives++
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return value.FloatString(18)
	}
	precision := twos
	if fives > precision {
		precision = fives
	}
	result := value.FloatString(precision)
	if strings.Contains(result, ".") {
		result = strings.TrimRight(strings.TrimRight(result, "0"), ".")
	}
	if result == "-0" {
		return "0"
	}
	return result
}

func canonicalUnit(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "元", "万元", "亿元", "人民币", "CNY", "RMB", "¥", "￥":
		return "CNY"
	case "美元", "USD":
		return "USD"
	case "%", "％", "PERCENT", "RATIO":
		return "PERCENT"
	case "PERCENTAGE_POINT", "个百分点", "个点", "PP":
		return "PERCENTAGE_POINT"
	default:
		return strings.TrimSpace(value)
	}
}

func timeMatches(text string, spec compiler.ResolvedTimeSpec) (bool, []string) {
	if compiler.ValidateResolvedTimeSpec(spec) != nil {
		return false, []string{"valid resolvedTimeSpec"}
	}
	location, _ := time.LoadLocation(spec.Timezone)
	currentStart, currentEnd := spec.ResolvedStart.In(location), spec.ResolvedEndExclusive.In(location)
	ranges := [][2]time.Time{{currentStart, currentEnd}}
	if spec.Comparison != nil {
		ranges = append(ranges, [2]time.Time{spec.Comparison.ResolvedStart.In(location), spec.Comparison.ResolvedEndExclusive.In(location)})
	}
	trimmed := strings.TrimSpace(text)
	if year, month, day, precision, ok := parseChineseOrISODate(trimmed, location); ok {
		for _, candidate := range ranges {
			switch precision {
			case "YEAR":
				if candidate[0].Year() == year && candidate[1].AddDate(0, 0, -1).Year() == year {
					return true, nil
				}
			case "MONTH":
				end := candidate[1].AddDate(0, 0, -1)
				if candidate[0].Year() == year && int(candidate[0].Month()) == month && end.Year() == year && int(end.Month()) == month {
					return true, nil
				}
			case "DAY":
				date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, location)
				if !date.Before(candidate[0]) && date.Before(candidate[1]) {
					return true, nil
				}
			}
		}
		return false, expectedTimeRanges(spec)
	}
	currentAliases := map[string][]string{
		"TODAY": {"今日", "今天", "当期", "本期"}, "YESTERDAY": {"昨日", "昨天", "当期", "本期"},
		"CURRENT_WEEK": {"本周", "当期", "本期"}, "PREVIOUS_WEEK": {"上周", "当期", "本期"},
		"CURRENT_MONTH": {"本月", "当期", "本期"}, "PREVIOUS_MONTH": {"上月", "当期", "本期"},
		"CURRENT_QUARTER": {"本季度", "当期", "本期"}, "PREVIOUS_QUARTER": {"上季度", "当期", "本期"},
		"CURRENT_YEAR": {"本年", "本年度", "当期", "本期"}, "PREVIOUS_YEAR": {"上年", "去年", "上年度", "当期", "本期"},
		"CURRENT_FISCAL_MONTH": {"本财月", "当期", "本期"}, "PREVIOUS_FISCAL_MONTH": {"上财月", "当期", "本期"},
		"CURRENT_FISCAL_QUARTER": {"本财季", "当期", "本期"}, "PREVIOUS_FISCAL_QUARTER": {"上财季", "当期", "本期"},
		"CURRENT_FISCAL_YEAR": {"本财年", "当期", "本期"}, "PREVIOUS_FISCAL_YEAR": {"上财年", "当期", "本期"},
	}
	if containsString(currentAliases[spec.RequestedPeriod], trimmed) {
		return true, nil
	}
	if spec.Comparison != nil {
		if containsString([]string{"同期", "对比期"}, trimmed) {
			return true, nil
		}
		if (trimmed == "上年同期" || trimmed == "去年同期") && strings.Contains(strings.ToUpper(spec.Comparison.Type), "YEAR") {
			return true, nil
		}
		if trimmed == "上月" && strings.Contains(strings.ToUpper(spec.Comparison.Type), "MONTH") {
			return true, nil
		}
	}
	return false, expectedTimeRanges(spec)
}

func parseChineseOrISODate(value string, location *time.Location) (int, int, int, string, bool) {
	patterns := []struct {
		re        *regexp.Regexp
		precision string
	}{
		{regexp.MustCompile(`^((?:19|20)[0-9]{2})年(0?[1-9]|1[0-2])月(0?[1-9]|[12][0-9]|3[01])日$`), "DAY"},
		{regexp.MustCompile(`^((?:19|20)[0-9]{2})[-/.](0?[1-9]|1[0-2])[-/.](0?[1-9]|[12][0-9]|3[01])$`), "DAY"},
		{regexp.MustCompile(`^((?:19|20)[0-9]{2})年(0?[1-9]|1[0-2])月$`), "MONTH"},
		{regexp.MustCompile(`^((?:19|20)[0-9]{2})[-/.](0?[1-9]|1[0-2])$`), "MONTH"},
		{regexp.MustCompile(`^((?:19|20)[0-9]{2})年$`), "YEAR"},
	}
	for _, pattern := range patterns {
		match := pattern.re.FindStringSubmatch(value)
		if match == nil {
			continue
		}
		year, _ := strconv.Atoi(match[1])
		month, day := 1, 1
		if len(match) > 2 {
			month, _ = strconv.Atoi(match[2])
		}
		if len(match) > 3 {
			day, _ = strconv.Atoi(match[3])
		}
		if _, err := time.ParseInLocation("2006-1-2", fmt.Sprintf("%d-%d-%d", year, month, day), location); err != nil {
			return 0, 0, 0, "", false
		}
		return year, month, day, pattern.precision, true
	}
	return 0, 0, 0, "", false
}

func expectedTimeRanges(spec compiler.ResolvedTimeSpec) []string {
	view := RenderTimeSpec(spec, RenderOptions{})
	result := []string{view.RangeLabel}
	if view.ComparisonLabel != "" {
		result = append(result, view.ComparisonLabel)
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	result := values[:0]
	for index, value := range values {
		if index == 0 || value != values[index-1] {
			result = append(result, value)
		}
	}
	return result
}
