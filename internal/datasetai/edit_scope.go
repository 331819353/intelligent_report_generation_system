package datasetai

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

const (
	datasetComponentID  = "dataset_1"
	endComponentID      = "end_1"
	maxChangeOperations = 64
	// One tableId migration may need 512 old REMOVE bindings plus 512 new
	// ADD/KEEP bindings, matching the maximum projection width of one node.
	maxFieldChanges      = 1024
	maxClarifyCandidates = 32
	maxChangeDescription = 500
	maxChangeDisplayName = 200
)

type promptEditContext struct {
	GroupRoles []promptGroupRole `json:"groupRoles,omitempty"`
}

type promptGroupRole struct {
	ID        string    `json:"id"`
	Input     PlanInput `json:"input"`
	Roles     []string  `json:"roles"`
	Consumers []string  `json:"consumers"`
}

// buildPromptEditContext exposes only topology derived from the trusted graph. Natural-language
// interpretation belongs to the intent model and must never be duplicated by local keywords.
func buildPromptEditContext(current *GraphPlan) *promptEditContext {
	if current == nil || len(current.Groups) == 0 {
		return nil
	}
	return &promptEditContext{GroupRoles: classifyPromptGroupRoles(*current)}
}

func classifyPromptGroupRoles(current GraphPlan) []promptGroupRole {
	result := make([]promptGroupRole, 0, len(current.Groups))
	for index, group := range current.Groups {
		roles := []string{fmt.Sprintf("POSITION_%d", index+1)}
		consumers := []string{}
		for _, join := range current.Joins {
			if join.Left == (PlanInput{Kind: "GROUP", ID: group.ID}) {
				roles = appendUniqueString(roles, "BEFORE_JOIN")
				consumers = append(consumers, "JOIN:"+join.ID+".left")
			}
			if join.Right == (PlanInput{Kind: "GROUP", ID: group.ID}) {
				roles = appendUniqueString(roles, "BEFORE_JOIN")
				consumers = append(consumers, "JOIN:"+join.ID+".right")
			}
		}
		if group.Input.Kind == "JOIN" {
			roles = appendUniqueString(roles, "AFTER_JOIN")
		}
		for _, downstream := range current.Groups {
			if downstream.Input == (PlanInput{Kind: "GROUP", ID: group.ID}) {
				consumers = append(consumers, "GROUP:"+downstream.ID+".input")
			}
		}
		if current.End.Input == (PlanInput{Kind: "GROUP", ID: group.ID}) {
			roles = appendUniqueString(roles, "OUTPUT_GROUP")
			consumers = append(consumers, "END:"+endComponentID+".input")
		}
		sort.Strings(consumers)
		result = append(result, promptGroupRole{
			ID: group.ID, Input: normalizeInput(group.Input), Roles: roles, Consumers: consumers,
		})
	}
	return result
}

var componentFields = map[string][]string{
	"DATASET":   {"name", "description"},
	"NODE":      {"tableId", "alias", "selectedColumns"},
	"JOIN":      {"name", "left", "right", "joinType", "conditions"},
	"GROUP":     {"name", "input", "dimensions", "metrics"},
	"TRANSFORM": {"name", "input", "family", "componentType", "rules"},
	"END":       {"name", "input", "outputs"},
}

var componentKindOrder = map[string]int{
	"DATASET":   0,
	"NODE":      1,
	"JOIN":      2,
	"GROUP":     3,
	"TRANSFORM": 4,
	"END":       5,
}

type componentSnapshot struct {
	Kind  string
	ID    string
	Name  string
	Value any
}

type consumerEdge struct {
	ConsumerKind string
	ConsumerID   string
	Field        string
	Input        PlanInput
}

// normalizeAndValidateChangeIntent validates only the structured contract emitted by the intent
// model. It deliberately has no instruction parameter and performs no language recognition.
func normalizeAndValidateChangeIntent(current GraphPlan, intent ChangeIntent, catalogs ...[]CatalogTable) (ChangeIntent, error) {
	current = normalizeGraphPlan(cloneGraphPlan(current))
	components, err := indexPlanComponents(current)
	if err != nil {
		return ChangeIntent{}, err
	}

	intent.Status = strings.ToUpper(strings.TrimSpace(intent.Status))
	intent.Question = strings.TrimSpace(intent.Question)
	if intent.Candidates == nil {
		intent.Candidates = []ComponentRef{}
	}
	if intent.ChangeSet.Operations == nil {
		intent.ChangeSet.Operations = []ChangeOperation{}
	}
	if intent.ChangeSet.FieldChanges == nil {
		intent.ChangeSet.FieldChanges = []FieldChange{}
	}

	switch intent.Status {
	case "CLARIFY":
		if !boundedText(intent.Question, 1, 500) {
			return ChangeIntent{}, invalidOutput("CLARIFY requires a question containing 1 to 500 characters")
		}
		if len(intent.ChangeSet.Operations) != 0 || len(intent.ChangeSet.FieldChanges) != 0 {
			return ChangeIntent{}, invalidOutput("CLARIFY must not contain change operations or field changes")
		}
		if len(intent.Candidates) > maxClarifyCandidates {
			return ChangeIntent{}, invalidOutput("CLARIFY contains too many candidates")
		}
		seen := map[string]bool{}
		for index := range intent.Candidates {
			candidate := &intent.Candidates[index]
			candidate.ComponentKind = strings.ToUpper(strings.TrimSpace(candidate.ComponentKind))
			candidate.ComponentID = strings.TrimSpace(candidate.ComponentID)
			key, keyErr := componentKey(candidate.ComponentKind, candidate.ComponentID)
			if keyErr != nil {
				return ChangeIntent{}, keyErr
			}
			if _, exists := components[key]; !exists {
				return ChangeIntent{}, invalidOutput(fmt.Sprintf("CLARIFY candidate %s:%s does not exist in current", candidate.ComponentKind, candidate.ComponentID))
			}
			if seen[key] {
				return ChangeIntent{}, invalidOutput(fmt.Sprintf("CLARIFY candidate %s:%s is duplicated", candidate.ComponentKind, candidate.ComponentID))
			}
			seen[key] = true
		}
		sort.Slice(intent.Candidates, func(i, j int) bool {
			return componentRefLess(intent.Candidates[i], intent.Candidates[j])
		})
		return intent, nil

	case "READY":
		if intent.Question != "" || len(intent.Candidates) != 0 {
			return ChangeIntent{}, invalidOutput("READY requires an empty question and no candidates")
		}
		changeSet, changeErr := normalizeAndValidateChangeSet(current, components, intent.ChangeSet, firstCatalog(catalogs))
		if changeErr != nil {
			return ChangeIntent{}, changeErr
		}
		intent.ChangeSet = changeSet
		return intent, nil

	default:
		return ChangeIntent{}, invalidOutput("change intent status must be READY or CLARIFY")
	}
}

func normalizeAndValidateChangeSet(current GraphPlan, components map[string]componentSnapshot, value ChangeSet, catalogs ...[]CatalogTable) (ChangeSet, error) {
	if value.Operations == nil {
		value.Operations = []ChangeOperation{}
	}
	if value.FieldChanges == nil {
		value.FieldChanges = []FieldChange{}
	}
	if len(value.Operations) > maxChangeOperations {
		return ChangeSet{}, invalidOutput("changeSet contains too many operations")
	}

	seenComponents := map[string]bool{}
	occupiedIDs := map[string]string{}
	for _, component := range components {
		occupiedIDs[component.ID] = component.Kind
	}
	addedIDs := map[string]string{}
	for index := range value.Operations {
		op := &value.Operations[index]
		op.Action = strings.ToUpper(strings.TrimSpace(op.Action))
		op.ComponentKind = strings.ToUpper(strings.TrimSpace(op.ComponentKind))
		op.ComponentID = strings.TrimSpace(op.ComponentID)
		op.ComponentName = strings.TrimSpace(op.ComponentName)
		op.Description = strings.TrimSpace(op.Description)
		if op.Fields == nil {
			op.Fields = []string{}
		}
		if op.InputChanges == nil {
			op.InputChanges = []InputChange{}
		}

		key, err := componentKey(op.ComponentKind, op.ComponentID)
		if err != nil {
			return ChangeSet{}, err
		}
		if seenComponents[key] {
			return ChangeSet{}, invalidOutput(fmt.Sprintf("changeSet contains conflicting operations for %s:%s", op.ComponentKind, op.ComponentID))
		}
		seenComponents[key] = true

		if op.Action != "ADD" && op.Action != "UPDATE" && op.Action != "REMOVE" {
			return ChangeSet{}, invalidOutput("change operation action must be ADD, UPDATE, or REMOVE")
		}
		currentComponent, exists := components[key]
		switch op.Action {
		case "ADD":
			if exists {
				return ChangeSet{}, invalidOutput(fmt.Sprintf("ADD target %s:%s already exists in current", op.ComponentKind, op.ComponentID))
			}
			if existingKind, occupied := occupiedIDs[op.ComponentID]; occupied {
				return ChangeSet{}, invalidOutput(fmt.Sprintf("ADD target %s:%s conflicts with current %s using the same id", op.ComponentKind, op.ComponentID, existingKind))
			}
			if existingKind, duplicated := addedIDs[op.ComponentID]; duplicated {
				return ChangeSet{}, invalidOutput(fmt.Sprintf("ADD target %s:%s conflicts with added %s using the same id", op.ComponentKind, op.ComponentID, existingKind))
			}
			addedIDs[op.ComponentID] = op.ComponentKind
			if op.ComponentKind == "DATASET" || op.ComponentKind == "END" {
				return ChangeSet{}, invalidOutput("DATASET and END cannot be added")
			}
		case "UPDATE":
			if !exists {
				return ChangeSet{}, invalidOutput(fmt.Sprintf("UPDATE target %s:%s does not exist in current", op.ComponentKind, op.ComponentID))
			}
		case "REMOVE":
			if !exists {
				return ChangeSet{}, invalidOutput(fmt.Sprintf("REMOVE target %s:%s does not exist in current", op.ComponentKind, op.ComponentID))
			}
			if op.ComponentKind == "DATASET" || op.ComponentKind == "END" {
				return ChangeSet{}, invalidOutput("DATASET and END cannot be removed")
			}
		}

		if !boundedText(op.ComponentName, 1, maxChangeDisplayName) {
			return ChangeSet{}, invalidOutput(fmt.Sprintf("change operation %s:%s has an invalid componentName", op.ComponentKind, op.ComponentID))
		}
		if !boundedText(op.Description, 1, maxChangeDescription) {
			return ChangeSet{}, invalidOutput(fmt.Sprintf("change operation %s:%s has an invalid description", op.ComponentKind, op.ComponentID))
		}
		if exists {
			op.ComponentName = currentComponent.Name
		}

		fields, err := normalizeOperationFields(*op)
		if err != nil {
			return ChangeSet{}, err
		}
		op.Fields = fields
		changes, err := normalizeOperationInputChanges(currentComponent, *op)
		if err != nil {
			return ChangeSet{}, err
		}
		op.InputChanges = changes
	}

	added := map[string]bool{}
	removed := map[string]bool{}
	operations := map[string]ChangeOperation{}
	for _, op := range value.Operations {
		key, _ := componentKey(op.ComponentKind, op.ComponentID)
		operations[key] = op
		if op.Action == "ADD" {
			added[key] = true
		}
		if op.Action == "REMOVE" {
			removed[key] = true
		}
	}
	for _, op := range value.Operations {
		for _, change := range op.InputChanges {
			toKey, _ := componentKey(change.To.Kind, change.To.ID)
			if _, exists := components[toKey]; !exists && !added[toKey] {
				return ChangeSet{}, invalidOutput(fmt.Sprintf("input change for %s:%s references unavailable target %s:%s", op.ComponentKind, op.ComponentID, change.To.Kind, change.To.ID))
			}
			if removed[toKey] {
				return ChangeSet{}, invalidOutput(fmt.Sprintf("input change for %s:%s points to removed %s:%s", op.ComponentKind, op.ComponentID, change.To.Kind, change.To.ID))
			}
		}
	}
	if err := validateRemovalConsumerUpdates(current, removed, operations); err != nil {
		return ChangeSet{}, err
	}
	fieldChanges, err := normalizeAndValidateFieldChanges(current, value.FieldChanges, operations, firstCatalog(catalogs))
	if err != nil {
		return ChangeSet{}, err
	}
	value.FieldChanges = fieldChanges

	sort.Slice(value.Operations, func(i, j int) bool { return changeOperationLess(value.Operations[i], value.Operations[j]) })
	return value, nil
}

func normalizeOperationFields(op ChangeOperation) ([]string, error) {
	if op.Action == "ADD" || op.Action == "REMOVE" {
		// ADD/REMOVE 已经锁定整个组件；模型偶尔重复列出新建或删除组件的
		// 内部字段。丢弃这些冗余声明不会扩大修改范围。
		return []string{}, nil
	}
	if len(op.Fields) == 0 {
		return nil, invalidOutput(fmt.Sprintf("UPDATE %s:%s requires at least one field", op.ComponentKind, op.ComponentID))
	}
	allowed := componentFields[op.ComponentKind]
	seen := map[string]bool{}
	for _, raw := range op.Fields {
		field := strings.TrimSpace(raw)
		if field == "" || !containsString(allowed, field) {
			return nil, invalidOutput(fmt.Sprintf("UPDATE %s:%s contains invalid field %q", op.ComponentKind, op.ComponentID, raw))
		}
		if seen[field] {
			return nil, invalidOutput(fmt.Sprintf("UPDATE %s:%s duplicates field %s", op.ComponentKind, op.ComponentID, field))
		}
		seen[field] = true
	}
	result := make([]string, 0, len(seen))
	for _, field := range allowed {
		if seen[field] {
			result = append(result, field)
		}
	}
	return result, nil
}

func normalizeOperationInputChanges(current componentSnapshot, op ChangeOperation) ([]InputChange, error) {
	if op.Action != "UPDATE" {
		// 新增/删除组件不通过 inputChanges 授权改线；真实消费者变化仍须由
		// 独立 UPDATE 声明，因此清空冗余值不会掩盖拓扑变化。
		return []InputChange{}, nil
	}

	inputFields := inputFieldsForKind(op.ComponentKind)
	required := map[string]bool{}
	for _, field := range op.Fields {
		if containsString(inputFields, field) {
			required[field] = true
		}
	}
	seen := map[string]bool{}
	result := make([]InputChange, 0, len(op.InputChanges))
	for _, raw := range op.InputChanges {
		change := raw
		change.Field = strings.TrimSpace(change.Field)
		change.From = normalizeInput(change.From)
		change.To = normalizeInput(change.To)
		if !required[change.Field] {
			return nil, invalidOutput(fmt.Sprintf("UPDATE %s:%s has an undeclared inputChange for %s", op.ComponentKind, op.ComponentID, change.Field))
		}
		if seen[change.Field] {
			return nil, invalidOutput(fmt.Sprintf("UPDATE %s:%s duplicates inputChange for %s", op.ComponentKind, op.ComponentID, change.Field))
		}
		seen[change.Field] = true
		from, ok := componentInputField(current, change.Field)
		if !ok || from != change.From {
			return nil, invalidOutput(fmt.Sprintf("UPDATE %s:%s inputChange.%s.from does not match current", op.ComponentKind, op.ComponentID, change.Field))
		}
		if err := validatePlanInputReference(change.To); err != nil {
			return nil, err
		}
		if change.From == change.To {
			return nil, invalidOutput(fmt.Sprintf("UPDATE %s:%s inputChange.%s does not change its input", op.ComponentKind, op.ComponentID, change.Field))
		}
		result = append(result, change)
	}
	for field := range required {
		if !seen[field] {
			return nil, invalidOutput(fmt.Sprintf("UPDATE %s:%s field %s requires an exact inputChange", op.ComponentKind, op.ComponentID, field))
		}
	}
	sort.Slice(result, func(i, j int) bool { return inputChangeLess(result[i], result[j]) })
	return result, nil
}

func validateRemovalConsumerUpdates(current GraphPlan, removed map[string]bool, operations map[string]ChangeOperation) error {
	for _, edge := range directConsumerEdges(current) {
		upstreamKey, err := componentKey(edge.Input.Kind, edge.Input.ID)
		if err != nil || !removed[upstreamKey] {
			continue
		}
		consumerKey, _ := componentKey(edge.ConsumerKind, edge.ConsumerID)
		if removed[consumerKey] {
			continue
		}
		op, exists := operations[consumerKey]
		if !exists || op.Action != "UPDATE" || !containsString(op.Fields, edge.Field) {
			return invalidOutput(fmt.Sprintf("removing %s:%s requires UPDATE %s:%s field %s", edge.Input.Kind, edge.Input.ID, edge.ConsumerKind, edge.ConsumerID, edge.Field))
		}
		matched := false
		for _, change := range op.InputChanges {
			if change.Field == edge.Field && change.From == normalizeInput(edge.Input) {
				matched = true
				break
			}
		}
		if !matched {
			return invalidOutput(fmt.Sprintf("removing %s:%s requires inputChange.from on %s:%s.%s", edge.Input.Kind, edge.Input.ID, edge.ConsumerKind, edge.ConsumerID, edge.Field))
		}
	}
	return nil
}

func normalizeAndValidateFieldChanges(current GraphPlan, values []FieldChange, operations map[string]ChangeOperation, catalog []CatalogTable) ([]FieldChange, error) {
	if values == nil {
		return []FieldChange{}, nil
	}
	if len(values) > maxFieldChanges {
		return nil, invalidOutput("changeSet contains too many fieldChanges")
	}
	if len(values) > 0 && len(catalog) == 0 {
		return nil, invalidOutput("fieldChanges require an authoritative asset catalog")
	}

	result := make([]FieldChange, 0, len(values))
	seen := map[string]bool{}
	currentNodeTables := map[string]string{}
	for _, node := range current.Nodes {
		currentNodeTables[node.ID] = node.TableID
	}
	claimedNodeTables := map[string]string{}
	migrationTargetTables := map[string]string{}
	claimBindingTable := func(binding FieldBinding) error {
		if currentTable, exists := currentNodeTables[binding.NodeID]; exists && operationMigratesNodeTable(operations, binding.NodeID) {
			if binding.TableID == currentTable {
				return nil
			}
			if target, claimed := migrationTargetTables[binding.NodeID]; claimed && target != binding.TableID {
				return invalidOutput(fmt.Sprintf("table migration for node %s targets conflicting tables %s and %s", binding.NodeID, target, binding.TableID))
			}
			migrationTargetTables[binding.NodeID] = binding.TableID
			return nil
		}
		if prior, exists := claimedNodeTables[binding.NodeID]; exists && prior != binding.TableID {
			return invalidOutput(fmt.Sprintf("fieldChanges bind node %s to conflicting tables %s and %s", binding.NodeID, prior, binding.TableID))
		}
		claimedNodeTables[binding.NodeID] = binding.TableID
		return nil
	}
	for _, raw := range values {
		value := raw
		value.Field = normalizeFieldBinding(value.Field)
		value.SelectionAction = strings.ToUpper(strings.TrimSpace(value.SelectionAction))
		value.Purpose = strings.ToUpper(strings.TrimSpace(value.Purpose))
		if value.GroupUses == nil {
			value.GroupUses = []FieldGroupUse{}
		}
		if value.JoinUses == nil {
			value.JoinUses = []FieldJoinUse{}
		}
		if value.OutputUses == nil {
			value.OutputUses = []FieldOutputUse{}
		}
		key := fieldBindingKey(value.Field)
		if key == "" || seen[key] {
			return nil, invalidOutput(fmt.Sprintf("fieldChange binding %s is invalid or duplicated", fieldBindingLabel(value.Field)))
		}
		seen[key] = true
		if err := claimBindingTable(value.Field); err != nil {
			return nil, err
		}
		column, err := validateCatalogFieldBinding(current, value.Field, catalog, operations)
		if err != nil {
			return nil, err
		}
		if value.SelectionAction != "ADD" && value.SelectionAction != "KEEP" && value.SelectionAction != "REMOVE" {
			return nil, invalidOutput(fmt.Sprintf("fieldChange %s selectionAction must be ADD, KEEP, or REMOVE", key))
		}
		if value.Purpose != "FINAL_OUTPUT" && value.Purpose != "INTERNAL_ONLY" && value.Purpose != "SELECTED_ONLY" {
			return nil, invalidOutput(fmt.Sprintf("fieldChange %s purpose must be FINAL_OUTPUT, INTERNAL_ONLY, or SELECTED_ONLY", fieldBindingLabel(value.Field)))
		}

		value.GroupUses, err = normalizeFieldGroupUses(current, value, column, operations)
		if err != nil {
			return nil, err
		}
		value.JoinUses, err = normalizeFieldJoinUses(current, value, catalog, operations)
		if err != nil {
			return nil, err
		}
		for _, use := range value.JoinUses {
			if err := claimBindingTable(use.Peer); err != nil {
				return nil, err
			}
		}
		value.OutputUses, err = normalizeFieldOutputUses(value)
		if err != nil {
			return nil, err
		}

		currentlySelected := planSelectsField(current, value.Field)
		tableMigration := operationMigratesNodeTable(operations, value.Field.NodeID)
		switch value.SelectionAction {
		case "ADD":
			if currentlySelected {
				return nil, invalidOutput(fmt.Sprintf("fieldChange ADD %s is already selected in current", fieldBindingLabel(value.Field)))
			}
			if !operationAllowsField(operations, "NODE", value.Field.NodeID, "selectedColumns") && !tableMigration {
				return nil, invalidOutput(fmt.Sprintf("fieldChange ADD %s requires ADD/UPDATE NODE:%s selectedColumns", fieldBindingLabel(value.Field), value.Field.NodeID))
			}
		case "KEEP":
			if !currentlySelected && !(tableMigration && planNodeSelectsColumn(current, value.Field.NodeID, value.Field.Column)) {
				return nil, invalidOutput(fmt.Sprintf("fieldChange KEEP %s is not selected in current", fieldBindingLabel(value.Field)))
			}
		case "REMOVE":
			if !currentlySelected {
				return nil, invalidOutput(fmt.Sprintf("fieldChange REMOVE %s is not selected in current", fieldBindingLabel(value.Field)))
			}
			if !operationAllowsField(operations, "NODE", value.Field.NodeID, "selectedColumns") && !tableMigration {
				return nil, invalidOutput(fmt.Sprintf("fieldChange REMOVE %s requires UPDATE NODE:%s selectedColumns", key, value.Field.NodeID))
			}
			if len(value.GroupUses)+len(value.JoinUses)+len(value.OutputUses) != 0 {
				return nil, invalidOutput(fmt.Sprintf("fieldChange REMOVE %s must have empty final uses", key))
			}
		}

		if value.SelectionAction != "REMOVE" {
			switch value.Purpose {
			case "FINAL_OUTPUT":
				if len(value.OutputUses) != 1 {
					return nil, invalidOutput(fmt.Sprintf("FINAL_OUTPUT fieldChange %s requires exactly one end output", key))
				}
			case "INTERNAL_ONLY":
				if len(value.OutputUses) != 0 || len(value.GroupUses)+len(value.JoinUses) == 0 {
					return nil, invalidOutput(fmt.Sprintf("INTERNAL_ONLY fieldChange %s requires a join/group use and no end output", key))
				}
			case "SELECTED_ONLY":
				if len(value.GroupUses)+len(value.JoinUses)+len(value.OutputUses) != 0 {
					return nil, invalidOutput(fmt.Sprintf("SELECTED_ONLY fieldChange %s requires all field uses to be empty", fieldBindingLabel(value.Field)))
				}
			}
		} else if value.Purpose == "SELECTED_ONLY" {
			return nil, invalidOutput(fmt.Sprintf("fieldChange REMOVE %s cannot use SELECTED_ONLY", fieldBindingLabel(value.Field)))
		}
		if err := validateFieldUseOperationCoverage(current, value, operations); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return fieldBindingLess(result[i].Field, result[j].Field) })
	return result, nil
}

func normalizeFieldGroupUses(current GraphPlan, fieldChange FieldChange, column CatalogColumn, operations map[string]ChangeOperation) ([]FieldGroupUse, error) {
	result := append([]FieldGroupUse{}, fieldChange.GroupUses...)
	seen := map[string]bool{}
	for index := range result {
		use := &result[index]
		use.GroupID = strings.TrimSpace(use.GroupID)
		use.Role = strings.ToUpper(strings.TrimSpace(use.Role))
		use.Grouping = strings.ToUpper(strings.TrimSpace(use.Grouping))
		use.Aggregation = strings.ToUpper(strings.TrimSpace(use.Aggregation))
		if !validIdentifier(use.GroupID) || seen[use.GroupID] || !componentExistsOrAdded(current, operations, "GROUP", use.GroupID) {
			return nil, invalidOutput(fmt.Sprintf("fieldChange %s has an invalid or duplicate groupUse %s", fieldBindingKey(fieldChange.Field), use.GroupID))
		}
		seen[use.GroupID] = true
		switch use.Role {
		case "DIMENSION":
			if use.Aggregation != "" || !oneOf(use.Grouping, "", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR") {
				return nil, invalidOutput(fmt.Sprintf("DIMENSION groupUse %s has invalid grouping/aggregation", use.GroupID))
			}
			if use.Grouping != "" && !isDateGroupingType(column.CanonicalType) {
				return nil, invalidOutput(fmt.Sprintf("DIMENSION groupUse %s requires a date field for grouping", use.GroupID))
			}
		case "METRIC":
			if use.Grouping != "" || !oneOf(use.Aggregation, "SUM", "AVG", "COUNT", "COUNT_DISTINCT", "MIN", "MAX") {
				return nil, invalidOutput(fmt.Sprintf("METRIC groupUse %s has invalid grouping/aggregation", use.GroupID))
			}
			if oneOf(use.Aggregation, "SUM", "AVG") && !isNumericCanonicalType(column.CanonicalType) {
				return nil, invalidOutput(fmt.Sprintf("METRIC groupUse %s requires a numeric field for SUM/AVG", use.GroupID))
			}
		default:
			return nil, invalidOutput(fmt.Sprintf("groupUse %s role must be DIMENSION or METRIC", use.GroupID))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GroupID < result[j].GroupID })
	return result, nil
}

func normalizeFieldJoinUses(current GraphPlan, fieldChange FieldChange, catalog []CatalogTable, operations map[string]ChangeOperation) ([]FieldJoinUse, error) {
	result := append([]FieldJoinUse{}, fieldChange.JoinUses...)
	seen := map[string]bool{}
	for index := range result {
		use := &result[index]
		use.JoinID = strings.TrimSpace(use.JoinID)
		use.Side = strings.ToUpper(strings.TrimSpace(use.Side))
		use.Peer = normalizeFieldBinding(use.Peer)
		key := use.JoinID + "\x00" + use.Side + "\x00" + fieldBindingKey(use.Peer)
		if !validIdentifier(use.JoinID) || (use.Side != "LEFT" && use.Side != "RIGHT") || seen[key] || !componentExistsOrAdded(current, operations, "JOIN", use.JoinID) {
			return nil, invalidOutput(fmt.Sprintf("fieldChange %s has an invalid or duplicate joinUse", fieldBindingKey(fieldChange.Field)))
		}
		seen[key] = true
		if _, err := validateCatalogFieldBinding(current, use.Peer, catalog, operations); err != nil {
			return nil, err
		}
		if use.Peer == fieldChange.Field {
			return nil, invalidOutput(fmt.Sprintf("fieldChange %s joinUse peer must be a different field", fieldBindingKey(fieldChange.Field)))
		}
	}
	sort.Slice(result, func(i, j int) bool { return fieldJoinUseLess(result[i], result[j]) })
	return result, nil
}

func normalizeFieldOutputUses(fieldChange FieldChange) ([]FieldOutputUse, error) {
	result := append([]FieldOutputUse{}, fieldChange.OutputUses...)
	if len(result) > 1 {
		return nil, invalidOutput(fmt.Sprintf("fieldChange %s may contain at most one outputUse", fieldBindingKey(fieldChange.Field)))
	}
	for index := range result {
		use := &result[index]
		use.EndID = strings.TrimSpace(use.EndID)
		use.Name = strings.TrimSpace(use.Name)
		use.Code = strings.TrimSpace(use.Code)
		if use.EndID != endComponentID || !boundedText(use.Name, 1, 200) || !validIdentifier(use.Code) {
			return nil, invalidOutput(fmt.Sprintf("fieldChange %s has an invalid outputUse", fieldBindingKey(fieldChange.Field)))
		}
	}
	return result, nil
}

func validateFieldUseOperationCoverage(current GraphPlan, desired FieldChange, operations map[string]ChangeOperation) error {
	currentGroups, currentJoins, currentOutputs := planFieldUses(current, desired.Field)
	currentGroupByID := map[string]FieldGroupUse{}
	desiredGroupByID := map[string]FieldGroupUse{}
	for _, use := range currentGroups {
		currentGroupByID[use.GroupID] = use
	}
	for _, use := range desired.GroupUses {
		desiredGroupByID[use.GroupID] = use
	}
	groupIDs := unionStringKeys(currentGroupByID, desiredGroupByID)
	for _, id := range groupIDs {
		before, beforeOK := currentGroupByID[id]
		after, afterOK := desiredGroupByID[id]
		if beforeOK == afterOK && (!beforeOK || before == after) {
			continue
		}
		fields := []string{}
		if beforeOK {
			fields = appendUniqueString(fields, groupUseComponentField(before))
		}
		if afterOK {
			fields = appendUniqueString(fields, groupUseComponentField(after))
		}
		for _, field := range fields {
			if !operationAllowsField(operations, "GROUP", id, field) && !operationMigratesNodeTable(operations, desired.Field.NodeID) {
				return invalidOutput(fmt.Sprintf("fieldChange %s requires GROUP:%s field %s in changeSet operations", fieldBindingKey(desired.Field), id, field))
			}
		}
	}

	if !reflect.DeepEqual(currentJoins, desired.JoinUses) {
		joinIDs := map[string]bool{}
		for _, use := range currentJoins {
			joinIDs[use.JoinID] = true
		}
		for _, use := range desired.JoinUses {
			joinIDs[use.JoinID] = true
		}
		for id := range joinIDs {
			if !reflect.DeepEqual(joinUsesForID(currentJoins, id), joinUsesForID(desired.JoinUses, id)) && !operationAllowsField(operations, "JOIN", id, "conditions") && !operationMigratesNodeTable(operations, desired.Field.NodeID) {
				return invalidOutput(fmt.Sprintf("fieldChange %s requires JOIN:%s field conditions in changeSet operations", fieldBindingKey(desired.Field), id))
			}
		}
	}
	if !reflect.DeepEqual(currentOutputs, desired.OutputUses) && !operationAllowsField(operations, "END", endComponentID, "outputs") && !operationMigratesNodeTable(operations, desired.Field.NodeID) {
		return invalidOutput(fmt.Sprintf("fieldChange %s requires END:%s field outputs in changeSet operations", fieldBindingKey(desired.Field), endComponentID))
	}
	if desired.SelectionAction == "KEEP" && !operationMigratesNodeTable(operations, desired.Field.NodeID) && reflect.DeepEqual(currentGroups, desired.GroupUses) && reflect.DeepEqual(currentJoins, desired.JoinUses) && reflect.DeepEqual(currentOutputs, desired.OutputUses) {
		return invalidOutput(fmt.Sprintf("fieldChange KEEP %s does not change any field use", fieldBindingKey(desired.Field)))
	}
	return nil
}

func validatePlanFieldChanges(current, proposal GraphPlan, expected []FieldChange, catalog []CatalogTable) error {
	if len(catalog) > 0 {
		for _, value := range expected {
			bindingPlan := proposal
			if value.SelectionAction == "REMOVE" {
				bindingPlan = current
			}
			if _, err := validateCatalogFieldBinding(bindingPlan, value.Field, catalog); err != nil {
				return err
			}
		}
	}
	if err := validateUnaffectedFieldOrder(current, proposal, expected); err != nil {
		return err
	}
	required := changedFieldRequirements(current, proposal)
	primary := map[string]bool{}
	for _, value := range expected {
		key := fieldBindingKey(value.Field)
		primary[key] = true
		if !required[key].any() {
			return invalidOutput(fmt.Sprintf("fieldChange %s does not correspond to a field-bearing plan change", fieldBindingLabel(value.Field)))
		}
	}
	peerJoinCoverage := changedJoinPeerCoverage(current, proposal, expected)
	for key, requirement := range required {
		// A selectedColumns membership change always needs its own FieldChange. A peer
		// declaration only describes the other end of one join condition and must never
		// authorize selecting or removing that peer field.
		if (requirement.Selection || requirement.Group || requirement.Output) && !primary[key] {
			return invalidOutput(fmt.Sprintf("plan changes field %s without a locked fieldChange", key))
		}
		if requirement.Join && !primary[key] && !peerJoinCoverage[key] {
			return invalidOutput(fmt.Sprintf("plan changes join use for field %s without a locked fieldChange", key))
		}
	}

	for _, value := range expected {
		key := fieldBindingKey(value.Field)
		currentSelected := planSelectsField(current, value.Field)
		proposalSelected := planSelectsField(proposal, value.Field)
		switch value.SelectionAction {
		case "ADD":
			if currentSelected || !proposalSelected {
				return invalidOutput(fmt.Sprintf("plan did not realize fieldChange ADD %s", key))
			}
		case "KEEP":
			if (!currentSelected && !logicalTableMigrationKeepsField(current, proposal, value.Field)) || !proposalSelected {
				return invalidOutput(fmt.Sprintf("plan did not preserve selected fieldChange KEEP %s", key))
			}
		case "REMOVE":
			if !currentSelected || proposalSelected {
				return invalidOutput(fmt.Sprintf("plan did not realize fieldChange REMOVE %s", key))
			}
		}
		groups, joins, outputs := planFieldUses(proposal, value.Field)
		if !reflect.DeepEqual(groups, value.GroupUses) || !reflect.DeepEqual(joins, value.JoinUses) || !reflect.DeepEqual(outputs, value.OutputUses) {
			return invalidOutput(fmt.Sprintf("plan field propagation differs from locked fieldChange %s", key))
		}
		if value.SelectionAction == "REMOVE" {
			continue
		}
		switch value.Purpose {
		case "FINAL_OUTPUT":
			if len(outputs) != 1 || !fieldAvailableAtInput(proposal, proposal.End.Input, value.Field, map[string]bool{}) {
				return invalidOutput(fmt.Sprintf("FINAL_OUTPUT field %s does not reach END through every group", key))
			}
		case "INTERNAL_ONLY":
			if len(outputs) != 0 || len(groups)+len(joins) == 0 {
				return invalidOutput(fmt.Sprintf("INTERNAL_ONLY field %s must remain in join/group use and outside END", key))
			}
		case "SELECTED_ONLY":
			if len(groups)+len(joins)+len(outputs) != 0 || !proposalSelected {
				return invalidOutput(fmt.Sprintf("SELECTED_ONLY field %s must remain selected without downstream uses", fieldBindingLabel(value.Field)))
			}
		}
	}
	return nil
}

// validateUnaffectedFieldOrder prevents a broad UPDATE authorization from being used to
// reorder or rewrite unrelated entries in the same nested array. A pure reorder remains a
// supported explicit operation when no fieldChanges are present.
func validateUnaffectedFieldOrder(current, proposal GraphPlan, expected []FieldChange) error {
	if len(expected) == 0 {
		return nil
	}
	primaryAffected := map[string]bool{}
	joinAffected := map[string]bool{}
	for _, value := range expected {
		key := fieldBindingKey(value.Field)
		primaryAffected[key] = true
		joinAffected[key] = true
		for _, use := range value.JoinUses {
			joinAffected[fieldBindingKey(use.Peer)] = true
		}
		_, currentJoins, _ := planFieldUses(current, value.Field)
		for _, use := range currentJoins {
			joinAffected[fieldBindingKey(use.Peer)] = true
		}
	}

	currentNodes := map[string]PlanNode{}
	proposalNodes := map[string]PlanNode{}
	for _, node := range current.Nodes {
		currentNodes[node.ID] = node
	}
	for _, node := range proposal.Nodes {
		proposalNodes[node.ID] = node
	}
	for id, before := range currentNodes {
		after, exists := proposalNodes[id]
		if !exists {
			continue
		}
		beforeColumns := filterUnaffectedColumns(before, primaryAffected)
		afterColumns := filterUnaffectedColumns(after, primaryAffected)
		if !reflect.DeepEqual(beforeColumns, afterColumns) {
			return invalidOutput(fmt.Sprintf("NODE:%s reorders or changes selectedColumns outside locked fieldChanges", id))
		}
	}

	currentGroups := map[string]PlanGroup{}
	proposalGroups := map[string]PlanGroup{}
	for _, group := range current.Groups {
		currentGroups[group.ID] = group
	}
	for _, group := range proposal.Groups {
		proposalGroups[group.ID] = group
	}
	for id, before := range currentGroups {
		after, exists := proposalGroups[id]
		if !exists {
			continue
		}
		if !reflect.DeepEqual(filterUnaffectedDimensions(current, before.Dimensions, primaryAffected), filterUnaffectedDimensions(proposal, after.Dimensions, primaryAffected)) ||
			!reflect.DeepEqual(filterUnaffectedMetrics(current, before.Metrics, primaryAffected), filterUnaffectedMetrics(proposal, after.Metrics, primaryAffected)) {
			return invalidOutput(fmt.Sprintf("GROUP:%s reorders or changes fields outside locked fieldChanges", id))
		}
	}

	currentJoins := map[string]PlanJoin{}
	proposalJoins := map[string]PlanJoin{}
	for _, join := range current.Joins {
		currentJoins[join.ID] = join
	}
	for _, join := range proposal.Joins {
		proposalJoins[join.ID] = join
	}
	for id, before := range currentJoins {
		after, exists := proposalJoins[id]
		if exists && !reflect.DeepEqual(filterUnaffectedConditions(current, before.Conditions, joinAffected), filterUnaffectedConditions(proposal, after.Conditions, joinAffected)) {
			return invalidOutput(fmt.Sprintf("JOIN:%s reorders or changes conditions outside locked fieldChanges", id))
		}
	}

	if !reflect.DeepEqual(filterUnaffectedOutputs(current, current.End.Outputs, primaryAffected), filterUnaffectedOutputs(proposal, proposal.End.Outputs, primaryAffected)) {
		return invalidOutput("END:end_1 reorders or changes outputs outside locked fieldChanges")
	}
	return nil
}

func filterUnaffectedColumns(node PlanNode, affected map[string]bool) []string {
	result := []string{}
	for _, column := range node.SelectedColumns {
		if !affected[fieldBindingKey(FieldBinding{NodeID: node.ID, TableID: node.TableID, Column: column})] {
			result = append(result, column)
		}
	}
	return result
}

func filterUnaffectedDimensions(plan GraphPlan, values []PlanDimension, affected map[string]bool) []PlanDimension {
	result := []PlanDimension{}
	for _, value := range values {
		if !affected[fieldBindingKey(planFieldBinding(plan, value.NodeID, value.Column))] {
			result = append(result, value)
		}
	}
	return result
}

func filterUnaffectedMetrics(plan GraphPlan, values []PlanMetric, affected map[string]bool) []PlanMetric {
	result := []PlanMetric{}
	for _, value := range values {
		if !affected[fieldBindingKey(planFieldBinding(plan, value.NodeID, value.Column))] {
			result = append(result, value)
		}
	}
	return result
}

func filterUnaffectedConditions(plan GraphPlan, values []PlanJoinCondition, affected map[string]bool) []PlanJoinCondition {
	result := []PlanJoinCondition{}
	for _, value := range values {
		left := fieldBindingKey(planFieldBinding(plan, value.LeftNodeID, value.LeftColumn))
		right := fieldBindingKey(planFieldBinding(plan, value.RightNodeID, value.RightColumn))
		if !affected[left] && !affected[right] {
			result = append(result, value)
		}
	}
	return result
}

func filterUnaffectedOutputs(plan GraphPlan, values []PlanOutput, affected map[string]bool) []PlanOutput {
	result := []PlanOutput{}
	for _, value := range values {
		if !affected[fieldBindingKey(planFieldBinding(plan, value.NodeID, value.Column))] {
			result = append(result, value)
		}
	}
	return result
}

type fieldChangeRequirement struct {
	Selection bool
	Group     bool
	Join      bool
	Output    bool
}

func (value fieldChangeRequirement) any() bool {
	return value.Selection || value.Group || value.Join || value.Output
}

func changedFieldRequirements(current, proposal GraphPlan) map[string]fieldChangeRequirement {
	result := map[string]fieldChangeRequirement{}
	bindings := map[string]FieldBinding{}
	currentNodes := map[string]PlanNode{}
	proposalNodes := map[string]PlanNode{}
	for _, node := range current.Nodes {
		currentNodes[node.ID] = node
	}
	for _, node := range proposal.Nodes {
		proposalNodes[node.ID] = node
	}
	for id, before := range currentNodes {
		after, exists := proposalNodes[id]
		if !exists {
			continue
		}
		beforeSet := stringSet(before.SelectedColumns)
		afterSet := stringSet(after.SelectedColumns)
		tableChanged := before.TableID != after.TableID
		for column := range beforeSet {
			binding := FieldBinding{NodeID: id, TableID: before.TableID, Column: column}
			bindings[fieldBindingKey(binding)] = binding
			if tableChanged || !afterSet[column] {
				key := fieldBindingKey(binding)
				requirement := result[key]
				requirement.Selection = true
				result[key] = requirement
			}
		}
		for column := range afterSet {
			binding := FieldBinding{NodeID: id, TableID: after.TableID, Column: column}
			bindings[fieldBindingKey(binding)] = binding
			if tableChanged || !beforeSet[column] {
				key := fieldBindingKey(binding)
				requirement := result[key]
				requirement.Selection = true
				result[key] = requirement
			}
		}
	}
	for id, node := range proposalNodes {
		if _, exists := currentNodes[id]; exists {
			continue
		}
		for _, column := range node.SelectedColumns {
			binding := FieldBinding{NodeID: id, TableID: node.TableID, Column: column}
			key := fieldBindingKey(binding)
			bindings[key] = binding
			requirement := result[key]
			requirement.Selection = true
			result[key] = requirement
		}
	}

	proposalJoinIDs := map[string]bool{}
	for _, join := range current.Joins {
		addConditionBindingValues(current, bindings, join.Conditions)
	}
	for _, join := range proposal.Joins {
		proposalJoinIDs[join.ID] = true
		addConditionBindingValues(proposal, bindings, join.Conditions)
	}

	proposalGroupIDs := map[string]bool{}
	for _, group := range current.Groups {
		addDimensionBindingValues(current, bindings, group.Dimensions)
		addMetricBindingValues(current, bindings, group.Metrics)
	}
	for _, group := range proposal.Groups {
		proposalGroupIDs[group.ID] = true
		addDimensionBindingValues(proposal, bindings, group.Dimensions)
		addMetricBindingValues(proposal, bindings, group.Metrics)
	}
	addOutputBindingValues(current, bindings, current.End.Outputs)
	addOutputBindingValues(proposal, bindings, proposal.End.Outputs)

	for key, binding := range bindings {
		// A whole REMOVE NODE authorizes the disappearing projection and every use tied
		// to that node. ADD NODE is the opposite: every selected field is explicit.
		if _, exists := proposalNodes[binding.NodeID]; !exists {
			continue
		}
		beforeGroups, beforeJoins, beforeOutputs := planFieldUses(current, binding)
		afterGroups, afterJoins, afterOutputs := planFieldUses(proposal, binding)

		// Removing a whole component is already explicitly locked by REMOVE and must
		// stay compatible with an empty fieldChanges array. Uses in newly added
		// components are included because their field roles are part of the new intent.
		beforeGroups = filterGroupUses(beforeGroups, proposalGroupIDs)
		afterGroups = filterGroupUses(afterGroups, proposalGroupIDs)
		beforeJoins = filterJoinUses(beforeJoins, proposalJoinIDs)
		afterJoins = filterJoinUses(afterJoins, proposalJoinIDs)

		requirement := result[key]
		if !reflect.DeepEqual(beforeGroups, afterGroups) {
			requirement.Group = true
		}
		if !reflect.DeepEqual(beforeJoins, afterJoins) {
			requirement.Join = true
		}
		if !reflect.DeepEqual(beforeOutputs, afterOutputs) {
			requirement.Output = true
		}
		if requirement.any() {
			result[key] = requirement
		}
	}
	return result
}

// changedJoinPeerCoverage grants the deliberately narrow exception in the contract: one
// FieldChange may describe both ends of the same changed join condition through joinUse.peer.
// It does not cover selectedColumns, group, or output changes on the peer.
func changedJoinPeerCoverage(current, proposal GraphPlan, expected []FieldChange) map[string]bool {
	result := map[string]bool{}
	for _, value := range expected {
		_, before, _ := planFieldUses(current, value.Field)
		_, after, _ := planFieldUses(proposal, value.Field)
		for _, changed := range symmetricJoinUseDifference(before, after) {
			peerKey := fieldBindingKey(changed.Peer)
			if peerKey == "" {
				continue
			}
			_, peerBefore, _ := planFieldUses(current, changed.Peer)
			_, peerAfter, _ := planFieldUses(proposal, changed.Peer)
			mirror := FieldJoinUse{
				JoinID: changed.JoinID,
				Side:   oppositeJoinSide(changed.Side),
				Peer:   value.Field,
			}
			if containsFieldJoinUse(symmetricJoinUseDifference(peerBefore, peerAfter), mirror) {
				result[peerKey] = true
			}
		}
	}
	return result
}

func planFieldUses(plan GraphPlan, binding FieldBinding) ([]FieldGroupUse, []FieldJoinUse, []FieldOutputUse) {
	groups := []FieldGroupUse{}
	joins := []FieldJoinUse{}
	outputs := []FieldOutputUse{}
	if !planHasBindingTable(plan, binding) {
		return groups, joins, outputs
	}
	for _, group := range plan.Groups {
		for _, dimension := range group.Dimensions {
			if dimension.NodeID == binding.NodeID && dimension.Column == binding.Column {
				groups = append(groups, FieldGroupUse{GroupID: group.ID, Role: "DIMENSION", Grouping: dimension.Grouping, Aggregation: ""})
			}
		}
		for _, metric := range group.Metrics {
			if metric.NodeID == binding.NodeID && metric.Column == binding.Column {
				groups = append(groups, FieldGroupUse{GroupID: group.ID, Role: "METRIC", Grouping: "", Aggregation: metric.Aggregation})
			}
		}
	}
	for _, join := range plan.Joins {
		for _, condition := range join.Conditions {
			if condition.LeftNodeID == binding.NodeID && condition.LeftColumn == binding.Column {
				joins = append(joins, FieldJoinUse{JoinID: join.ID, Side: "LEFT", Peer: planFieldBinding(plan, condition.RightNodeID, condition.RightColumn)})
			}
			if condition.RightNodeID == binding.NodeID && condition.RightColumn == binding.Column {
				joins = append(joins, FieldJoinUse{JoinID: join.ID, Side: "RIGHT", Peer: planFieldBinding(plan, condition.LeftNodeID, condition.LeftColumn)})
			}
		}
	}
	for _, output := range plan.End.Outputs {
		if output.NodeID == binding.NodeID && output.Column == binding.Column {
			outputs = append(outputs, FieldOutputUse{EndID: endComponentID, Name: output.Name, Code: output.Code})
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].GroupID < groups[j].GroupID })
	sort.Slice(joins, func(i, j int) bool { return fieldJoinUseLess(joins[i], joins[j]) })
	return groups, joins, outputs
}

func fieldAvailableAtInput(plan GraphPlan, input PlanInput, binding FieldBinding, visiting map[string]bool) bool {
	key := input.Kind + ":" + input.ID
	if visiting[key] {
		return false
	}
	visiting[key] = true
	defer delete(visiting, key)
	switch input.Kind {
	case "NODE":
		return input.ID == binding.NodeID && planSelectsField(plan, binding)
	case "GROUP":
		for _, group := range plan.Groups {
			if group.ID != input.ID || !fieldAvailableAtInput(plan, group.Input, binding, visiting) {
				continue
			}
			for _, dimension := range group.Dimensions {
				if dimension.NodeID == binding.NodeID && dimension.Column == binding.Column {
					return true
				}
			}
			for _, metric := range group.Metrics {
				if metric.NodeID == binding.NodeID && metric.Column == binding.Column {
					return true
				}
			}
		}
	case "TRANSFORM":
		for _, transform := range plan.Transforms {
			if transform.ID != input.ID || !fieldAvailableAtInput(plan, transform.Input, binding, visiting) {
				continue
			}
			physicalKey := fieldKey(binding.NodeID, binding.Column)
			for _, rule := range transform.Rules {
				if rule.ReplaceSourceKey == physicalKey {
					return false
				}
			}
			return true
		}
	case "JOIN":
		for _, join := range plan.Joins {
			if join.ID == input.ID {
				return fieldAvailableAtInput(plan, join.Left, binding, visiting) || fieldAvailableAtInput(plan, join.Right, binding, visiting)
			}
		}
	}
	return false
}

// validateAndCanonicalizePlanChanges independently computes the actual graph diff and requires it
// to match the locked change set exactly. Human-readable names come from trusted graph structures;
// intent descriptions are preserved only after the scope matches.
func validateAndCanonicalizePlanChanges(current, proposal GraphPlan, expected ChangeSet, catalogs ...[]CatalogTable) (ChangeSet, error) {
	current = normalizeGraphPlan(cloneGraphPlan(current))
	proposal = normalizeGraphPlan(cloneGraphPlan(proposal))
	// 新建画布在用户填写保存信息前也走 MODIFY 两阶段协议。空名称不是一个
	// 需要保护的既有业务值；用候选名称补齐比较基线，避免把必需初始化误报
	// 为未授权 DATASET 更新。已有非空名称仍按原值严格比较。
	if current.Dataset.Name == "" {
		current.Dataset.Name = proposal.Dataset.Name
	}
	currentComponents, err := indexPlanComponents(current)
	if err != nil {
		return ChangeSet{}, err
	}
	expected, err = normalizeAndValidateChangeSet(current, currentComponents, expected, firstCatalog(catalogs))
	if err != nil {
		return ChangeSet{}, err
	}
	proposalComponents, err := indexPlanComponents(proposal)
	if err != nil {
		return ChangeSet{}, err
	}
	actual := calculatePlanChanges(currentComponents, proposalComponents)
	if !equalChangeOperationScope(actual, expected) {
		return ChangeSet{}, changeSetMismatchError(actual, expected)
	}
	if err := validatePlanFieldChanges(current, proposal, expected.FieldChanges, firstCatalog(catalogs)); err != nil {
		return ChangeSet{}, err
	}
	actual.FieldChanges = cloneFieldChanges(expected.FieldChanges)

	descriptions := map[string]string{}
	for _, op := range expected.Operations {
		descriptions[operationScopeKey(op)] = op.Description
	}
	for index := range actual.Operations {
		actual.Operations[index].Description = descriptions[operationScopeKey(actual.Operations[index])]
	}
	return actual, nil
}

func calculatePlanChanges(current, proposal map[string]componentSnapshot) ChangeSet {
	operations := []ChangeOperation{}
	keys := make([]string, 0, len(current)+len(proposal))
	seen := map[string]bool{}
	for key := range current {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range proposal {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return snapshotKeyLess(keys[i], keys[j], current, proposal) })
	for _, key := range keys {
		before, hadBefore := current[key]
		after, hasAfter := proposal[key]
		switch {
		case !hadBefore && hasAfter:
			operations = append(operations, ChangeOperation{
				Action: "ADD", ComponentKind: after.Kind, ComponentID: after.ID, ComponentName: after.Name,
				Fields: []string{}, InputChanges: []InputChange{}, Description: "",
			})
		case hadBefore && !hasAfter:
			operations = append(operations, ChangeOperation{
				Action: "REMOVE", ComponentKind: before.Kind, ComponentID: before.ID, ComponentName: before.Name,
				Fields: []string{}, InputChanges: []InputChange{}, Description: "",
			})
		case hadBefore && hasAfter:
			fields := changedComponentFields(before, after)
			if len(fields) == 0 {
				continue
			}
			changes := changedComponentInputs(before, after, fields)
			operations = append(operations, ChangeOperation{
				Action: "UPDATE", ComponentKind: after.Kind, ComponentID: after.ID, ComponentName: after.Name,
				Fields: fields, InputChanges: changes, Description: "",
			})
		}
	}
	return ChangeSet{Operations: operations, FieldChanges: []FieldChange{}}
}

func equalChangeSetScope(left, right ChangeSet) bool {
	if !equalChangeOperationScope(left, right) {
		return false
	}
	return reflect.DeepEqual(canonicalFieldChanges(left.FieldChanges), canonicalFieldChanges(right.FieldChanges))
}

func equalChangeOperationScope(left, right ChangeSet) bool {
	leftOps := canonicalScopeOperations(left.Operations)
	rightOps := canonicalScopeOperations(right.Operations)
	return reflect.DeepEqual(leftOps, rightOps)
}

func canonicalScopeOperations(values []ChangeOperation) []ChangeOperation {
	result := make([]ChangeOperation, len(values))
	for index, raw := range values {
		op := raw
		op.Action = strings.ToUpper(strings.TrimSpace(op.Action))
		op.ComponentKind = strings.ToUpper(strings.TrimSpace(op.ComponentKind))
		op.ComponentID = strings.TrimSpace(op.ComponentID)
		op.ComponentName = ""
		op.Description = ""
		op.Fields = append([]string(nil), op.Fields...)
		for fieldIndex := range op.Fields {
			op.Fields[fieldIndex] = strings.TrimSpace(op.Fields[fieldIndex])
		}
		sort.Strings(op.Fields)
		if op.Fields == nil {
			op.Fields = []string{}
		}
		op.InputChanges = append([]InputChange(nil), op.InputChanges...)
		for changeIndex := range op.InputChanges {
			op.InputChanges[changeIndex].Field = strings.TrimSpace(op.InputChanges[changeIndex].Field)
			op.InputChanges[changeIndex].From = normalizeInput(op.InputChanges[changeIndex].From)
			op.InputChanges[changeIndex].To = normalizeInput(op.InputChanges[changeIndex].To)
		}
		sort.Slice(op.InputChanges, func(i, j int) bool { return inputChangeLess(op.InputChanges[i], op.InputChanges[j]) })
		if op.InputChanges == nil {
			op.InputChanges = []InputChange{}
		}
		result[index] = op
	}
	sort.Slice(result, func(i, j int) bool { return changeOperationLess(result[i], result[j]) })
	return result
}

func changeSetMismatchError(actual, expected ChangeSet) error {
	actualByKey := map[string]ChangeOperation{}
	expectedByKey := map[string]ChangeOperation{}
	for _, op := range canonicalScopeOperations(actual.Operations) {
		actualByKey[operationIdentityKey(op)] = op
	}
	for _, op := range canonicalScopeOperations(expected.Operations) {
		expectedByKey[operationIdentityKey(op)] = op
	}
	keys := make([]string, 0, len(actualByKey)+len(expectedByKey))
	seen := map[string]bool{}
	for key := range actualByKey {
		keys = append(keys, key)
		seen[key] = true
	}
	for key := range expectedByKey {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		actualOp, actualExists := actualByKey[key]
		expectedOp, expectedExists := expectedByKey[key]
		switch {
		case actualExists && !expectedExists:
			return invalidOutput(fmt.Sprintf("plan contains undeclared %s %s:%s", actualOp.Action, actualOp.ComponentKind, actualOp.ComponentID))
		case !actualExists && expectedExists:
			return invalidOutput(fmt.Sprintf("plan did not realize locked %s %s:%s", expectedOp.Action, expectedOp.ComponentKind, expectedOp.ComponentID))
		case !reflect.DeepEqual(actualOp.Fields, expectedOp.Fields):
			return invalidOutput(fmt.Sprintf("plan changed fields outside locked scope for %s %s:%s: actual=%v expected=%v", actualOp.Action, actualOp.ComponentKind, actualOp.ComponentID, actualOp.Fields, expectedOp.Fields))
		case !reflect.DeepEqual(actualOp.InputChanges, expectedOp.InputChanges):
			return invalidOutput(fmt.Sprintf("plan input rewiring differs from locked scope for %s:%s", actualOp.ComponentKind, actualOp.ComponentID))
		}
	}
	return invalidOutput("plan changes differ from locked changeSet")
}

func indexPlanComponents(plan GraphPlan) (map[string]componentSnapshot, error) {
	result := map[string]componentSnapshot{}
	seenIDs := map[string]string{}
	add := func(component componentSnapshot) error {
		key, err := componentKey(component.Kind, component.ID)
		if err != nil {
			return err
		}
		if prior, exists := seenIDs[component.ID]; exists {
			return invalidOutput(fmt.Sprintf("component id %s is shared by %s and %s", component.ID, prior, component.Kind))
		}
		seenIDs[component.ID] = component.Kind
		result[key] = component
		return nil
	}
	if err := add(componentSnapshot{Kind: "DATASET", ID: datasetComponentID, Name: plan.Dataset.Name, Value: plan.Dataset}); err != nil {
		return nil, err
	}
	for _, node := range plan.Nodes {
		if err := add(componentSnapshot{Kind: "NODE", ID: node.ID, Name: node.Alias, Value: node}); err != nil {
			return nil, err
		}
	}
	for _, join := range plan.Joins {
		if err := add(componentSnapshot{Kind: "JOIN", ID: join.ID, Name: join.Name, Value: join}); err != nil {
			return nil, err
		}
	}
	for _, group := range plan.Groups {
		if err := add(componentSnapshot{Kind: "GROUP", ID: group.ID, Name: group.Name, Value: group}); err != nil {
			return nil, err
		}
	}
	for _, transform := range plan.Transforms {
		if err := add(componentSnapshot{Kind: "TRANSFORM", ID: transform.ID, Name: transform.Name, Value: transform}); err != nil {
			return nil, err
		}
	}
	if err := add(componentSnapshot{Kind: "END", ID: endComponentID, Name: plan.End.Name, Value: plan.End}); err != nil {
		return nil, err
	}
	return result, nil
}

func changedComponentFields(before, after componentSnapshot) []string {
	result := []string{}
	for _, field := range componentFields[before.Kind] {
		if !componentFieldEqual(before, after, field) {
			result = append(result, field)
		}
	}
	return result
}

func componentFieldEqual(before, after componentSnapshot, field string) bool {
	switch before.Kind {
	case "DATASET":
		left, right := before.Value.(PlanDataset), after.Value.(PlanDataset)
		switch field {
		case "name":
			return left.Name == right.Name
		case "description":
			return left.Description == right.Description
		}
	case "NODE":
		left, right := before.Value.(PlanNode), after.Value.(PlanNode)
		switch field {
		case "tableId":
			return left.TableID == right.TableID
		case "alias":
			return left.Alias == right.Alias
		case "selectedColumns":
			return reflect.DeepEqual(left.SelectedColumns, right.SelectedColumns)
		}
	case "JOIN":
		left, right := before.Value.(PlanJoin), after.Value.(PlanJoin)
		switch field {
		case "name":
			return left.Name == right.Name
		case "left":
			return left.Left == right.Left
		case "right":
			return left.Right == right.Right
		case "joinType":
			return left.JoinType == right.JoinType
		case "conditions":
			return reflect.DeepEqual(left.Conditions, right.Conditions)
		}
	case "GROUP":
		left, right := before.Value.(PlanGroup), after.Value.(PlanGroup)
		switch field {
		case "name":
			return left.Name == right.Name
		case "input":
			return left.Input == right.Input
		case "dimensions":
			return reflect.DeepEqual(left.Dimensions, right.Dimensions)
		case "metrics":
			return reflect.DeepEqual(left.Metrics, right.Metrics)
		}
	case "TRANSFORM":
		left, right := before.Value.(PlanTransform), after.Value.(PlanTransform)
		switch field {
		case "name":
			return left.Name == right.Name
		case "input":
			return left.Input == right.Input
		case "family":
			return left.Family == right.Family
		case "componentType":
			return left.ComponentType == right.ComponentType
		case "rules":
			return reflect.DeepEqual(left.Rules, right.Rules)
		}
	case "END":
		left, right := before.Value.(PlanEnd), after.Value.(PlanEnd)
		switch field {
		case "name":
			return left.Name == right.Name
		case "input":
			return left.Input == right.Input
		case "outputs":
			return reflect.DeepEqual(left.Outputs, right.Outputs)
		}
	}
	return false
}

func changedComponentInputs(before, after componentSnapshot, fields []string) []InputChange {
	result := []InputChange{}
	for _, field := range fields {
		from, fromOK := componentInputField(before, field)
		to, toOK := componentInputField(after, field)
		if fromOK && toOK {
			result = append(result, InputChange{Field: field, From: from, To: to})
		}
	}
	return result
}

func componentInputField(component componentSnapshot, field string) (PlanInput, bool) {
	switch value := component.Value.(type) {
	case PlanJoin:
		if field == "left" {
			return normalizeInput(value.Left), true
		}
		if field == "right" {
			return normalizeInput(value.Right), true
		}
	case PlanGroup:
		if field == "input" {
			return normalizeInput(value.Input), true
		}
	case PlanTransform:
		if field == "input" {
			return normalizeInput(value.Input), true
		}
	case PlanEnd:
		if field == "input" {
			return normalizeInput(value.Input), true
		}
	}
	return PlanInput{}, false
}

func directConsumerEdges(plan GraphPlan) []consumerEdge {
	result := []consumerEdge{}
	for _, join := range plan.Joins {
		result = append(result,
			consumerEdge{ConsumerKind: "JOIN", ConsumerID: join.ID, Field: "left", Input: normalizeInput(join.Left)},
			consumerEdge{ConsumerKind: "JOIN", ConsumerID: join.ID, Field: "right", Input: normalizeInput(join.Right)},
		)
	}
	for _, group := range plan.Groups {
		result = append(result, consumerEdge{ConsumerKind: "GROUP", ConsumerID: group.ID, Field: "input", Input: normalizeInput(group.Input)})
	}
	for _, transform := range plan.Transforms {
		result = append(result, consumerEdge{ConsumerKind: "TRANSFORM", ConsumerID: transform.ID, Field: "input", Input: normalizeInput(transform.Input)})
	}
	result = append(result, consumerEdge{ConsumerKind: "END", ConsumerID: endComponentID, Field: "input", Input: normalizeInput(plan.End.Input)})
	return result
}

func componentKey(kind, id string) (string, error) {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	id = strings.TrimSpace(id)
	if _, allowed := componentFields[kind]; !allowed || !validIdentifier(id) {
		return "", invalidOutput(fmt.Sprintf("invalid component reference %s:%s", kind, id))
	}
	if kind == "DATASET" && id != datasetComponentID {
		return "", invalidOutput("DATASET component id must be dataset_1")
	}
	if kind == "END" && id != endComponentID {
		return "", invalidOutput("END component id must be end_1")
	}
	return kind + "\x00" + id, nil
}

func validatePlanInputReference(value PlanInput) error {
	if value.Kind != "NODE" && value.Kind != "JOIN" && value.Kind != "GROUP" && value.Kind != "TRANSFORM" {
		return invalidOutput("input reference kind must be NODE, JOIN, GROUP, or TRANSFORM")
	}
	if !validIdentifier(value.ID) {
		return invalidOutput(fmt.Sprintf("input reference %s:%s has an invalid id", value.Kind, value.ID))
	}
	return nil
}

func inputFieldsForKind(kind string) []string {
	switch kind {
	case "JOIN":
		return []string{"left", "right"}
	case "GROUP", "TRANSFORM", "END":
		return []string{"input"}
	default:
		return []string{}
	}
}

func operationIdentityKey(op ChangeOperation) string {
	return op.Action + "\x00" + op.ComponentKind + "\x00" + op.ComponentID
}

func operationScopeKey(op ChangeOperation) string {
	canonical := canonicalScopeOperations([]ChangeOperation{op})[0]
	parts := []string{operationIdentityKey(canonical), strings.Join(canonical.Fields, ",")}
	for _, change := range canonical.InputChanges {
		parts = append(parts, change.Field+":"+change.From.Kind+":"+change.From.ID+">"+change.To.Kind+":"+change.To.ID)
	}
	return strings.Join(parts, "\x01")
}

func changeOperationLess(left, right ChangeOperation) bool {
	leftRank, rightRank := componentKindOrder[left.ComponentKind], componentKindOrder[right.ComponentKind]
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	if left.ComponentID != right.ComponentID {
		return left.ComponentID < right.ComponentID
	}
	return left.Action < right.Action
}

func componentRefLess(left, right ComponentRef) bool {
	leftRank, rightRank := componentKindOrder[left.ComponentKind], componentKindOrder[right.ComponentKind]
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	return left.ComponentID < right.ComponentID
}

func inputChangeLess(left, right InputChange) bool {
	if left.Field != right.Field {
		return left.Field < right.Field
	}
	if left.From.Kind != right.From.Kind {
		return left.From.Kind < right.From.Kind
	}
	if left.From.ID != right.From.ID {
		return left.From.ID < right.From.ID
	}
	if left.To.Kind != right.To.Kind {
		return left.To.Kind < right.To.Kind
	}
	return left.To.ID < right.To.ID
}

func snapshotKeyLess(leftKey, rightKey string, current, proposal map[string]componentSnapshot) bool {
	left, exists := current[leftKey]
	if !exists {
		left = proposal[leftKey]
	}
	right, exists := current[rightKey]
	if !exists {
		right = proposal[rightKey]
	}
	return componentRefLess(
		ComponentRef{ComponentKind: left.Kind, ComponentID: left.ID},
		ComponentRef{ComponentKind: right.Kind, ComponentID: right.ID},
	)
}

func firstCatalog(values [][]CatalogTable) []CatalogTable {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

func normalizeFieldBinding(value FieldBinding) FieldBinding {
	value.NodeID = strings.TrimSpace(value.NodeID)
	value.TableID = strings.TrimSpace(value.TableID)
	value.Column = strings.TrimSpace(value.Column)
	return value
}

func fieldBindingKey(value FieldBinding) string {
	value = normalizeFieldBinding(value)
	if !validIdentifier(value.NodeID) || !boundedText(value.TableID, 1, 128) || !validPhysicalIdentifier(value.Column) {
		return ""
	}
	return fieldBindingLabel(value)
}

func fieldBindingLabel(value FieldBinding) string {
	value = normalizeFieldBinding(value)
	return value.NodeID + "@" + value.TableID + "." + value.Column
}

func fieldBindingLess(left, right FieldBinding) bool {
	left, right = normalizeFieldBinding(left), normalizeFieldBinding(right)
	if left.NodeID != right.NodeID {
		return left.NodeID < right.NodeID
	}
	if left.TableID != right.TableID {
		return left.TableID < right.TableID
	}
	return left.Column < right.Column
}

func fieldJoinUseLess(left, right FieldJoinUse) bool {
	if left.JoinID != right.JoinID {
		return left.JoinID < right.JoinID
	}
	if left.Side != right.Side {
		return left.Side < right.Side
	}
	return fieldBindingLess(left.Peer, right.Peer)
}

func validateCatalogFieldBinding(plan GraphPlan, binding FieldBinding, catalog []CatalogTable, operationSets ...map[string]ChangeOperation) (CatalogColumn, error) {
	binding = normalizeFieldBinding(binding)
	key := fieldBindingKey(binding)
	if key == "" {
		return CatalogColumn{}, invalidOutput(fmt.Sprintf("invalid field binding %s", fieldBindingLabel(binding)))
	}
	var node *PlanNode
	for index := range plan.Nodes {
		if plan.Nodes[index].ID == binding.NodeID {
			node = &plan.Nodes[index]
			break
		}
	}
	if node == nil {
		allowedAddedNode := false
		if len(operationSets) > 0 {
			componentKey, _ := componentKey("NODE", binding.NodeID)
			operation, exists := operationSets[0][componentKey]
			allowedAddedNode = exists && operation.Action == "ADD"
		}
		if !allowedAddedNode {
			return CatalogColumn{}, invalidOutput(fmt.Sprintf("field binding %s references an unavailable node", fieldBindingLabel(binding)))
		}
	} else if node.TableID != binding.TableID && !(len(operationSets) > 0 && operationMigratesNodeTable(operationSets[0], binding.NodeID)) {
		return CatalogColumn{}, invalidOutput(fmt.Sprintf("field binding %s does not match NODE:%s tableId %s", fieldBindingLabel(binding), binding.NodeID, node.TableID))
	}
	for _, table := range catalog {
		if table.ID != binding.TableID {
			continue
		}
		for _, column := range table.Columns {
			if column.Name == binding.Column {
				return column, nil
			}
		}
		return CatalogColumn{}, invalidOutput(fmt.Sprintf("field binding %s is unavailable in the authoritative catalog", fieldBindingLabel(binding)))
	}
	return CatalogColumn{}, invalidOutput(fmt.Sprintf("field binding %s references an unavailable table", fieldBindingLabel(binding)))
}

func componentExistsOrAdded(current GraphPlan, operations map[string]ChangeOperation, kind, id string) bool {
	key, err := componentKey(kind, id)
	if err != nil {
		return false
	}
	if operation, exists := operations[key]; exists {
		return operation.Action != "REMOVE"
	}
	components, err := indexPlanComponents(current)
	if err != nil {
		return false
	}
	_, exists := components[key]
	return exists
}

func operationAllowsField(operations map[string]ChangeOperation, kind, id, field string) bool {
	key, err := componentKey(kind, id)
	if err != nil {
		return false
	}
	operation, exists := operations[key]
	if !exists {
		return false
	}
	if operation.Action == "ADD" || operation.Action == "REMOVE" {
		return true
	}
	return operation.Action == "UPDATE" && containsString(operation.Fields, field)
}

func operationMigratesNodeTable(operations map[string]ChangeOperation, nodeID string) bool {
	key, err := componentKey("NODE", nodeID)
	if err != nil {
		return false
	}
	operation, exists := operations[key]
	return exists && operation.Action == "UPDATE" && containsString(operation.Fields, "tableId")
}

func planNodeSelectsColumn(plan GraphPlan, nodeID, column string) bool {
	for _, node := range plan.Nodes {
		if node.ID == nodeID {
			return containsString(node.SelectedColumns, column)
		}
	}
	return false
}

func logicalTableMigrationKeepsField(current, proposal GraphPlan, binding FieldBinding) bool {
	binding = normalizeFieldBinding(binding)
	var currentNode, proposalNode *PlanNode
	for index := range current.Nodes {
		if current.Nodes[index].ID == binding.NodeID {
			currentNode = &current.Nodes[index]
			break
		}
	}
	for index := range proposal.Nodes {
		if proposal.Nodes[index].ID == binding.NodeID {
			proposalNode = &proposal.Nodes[index]
			break
		}
	}
	return currentNode != nil && proposalNode != nil &&
		currentNode.TableID != proposalNode.TableID && proposalNode.TableID == binding.TableID &&
		containsString(currentNode.SelectedColumns, binding.Column) && containsString(proposalNode.SelectedColumns, binding.Column)
}

func planSelectsField(plan GraphPlan, binding FieldBinding) bool {
	binding = normalizeFieldBinding(binding)
	for _, node := range plan.Nodes {
		if node.ID == binding.NodeID && node.TableID == binding.TableID && containsString(node.SelectedColumns, binding.Column) {
			return true
		}
	}
	return false
}

func groupUseComponentField(value FieldGroupUse) string {
	if value.Role == "DIMENSION" {
		return "dimensions"
	}
	return "metrics"
}

func joinUsesForID(values []FieldJoinUse, id string) []FieldJoinUse {
	result := []FieldJoinUse{}
	for _, value := range values {
		if value.JoinID == id {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return fieldJoinUseLess(result[i], result[j]) })
	return result
}

func unionStringKeys[V any](values ...map[string]V) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		for key := range value {
			if !seen[key] {
				seen[key] = true
				result = append(result, key)
			}
		}
	}
	sort.Strings(result)
	return result
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func planFieldBinding(plan GraphPlan, nodeID, column string) FieldBinding {
	for _, node := range plan.Nodes {
		if node.ID == nodeID {
			return normalizeFieldBinding(FieldBinding{NodeID: nodeID, TableID: node.TableID, Column: column})
		}
	}
	return normalizeFieldBinding(FieldBinding{NodeID: nodeID, Column: column})
}

func planHasBindingTable(plan GraphPlan, binding FieldBinding) bool {
	binding = normalizeFieldBinding(binding)
	for _, node := range plan.Nodes {
		if node.ID == binding.NodeID {
			return node.TableID == binding.TableID
		}
	}
	return false
}

func addConditionBindingValues(plan GraphPlan, result map[string]FieldBinding, values []PlanJoinCondition) {
	for _, value := range values {
		addBindingValue(result, planFieldBinding(plan, value.LeftNodeID, value.LeftColumn))
		addBindingValue(result, planFieldBinding(plan, value.RightNodeID, value.RightColumn))
	}
}

func addDimensionBindingValues(plan GraphPlan, result map[string]FieldBinding, values []PlanDimension) {
	for _, value := range values {
		addBindingValue(result, planFieldBinding(plan, value.NodeID, value.Column))
	}
}

func addMetricBindingValues(plan GraphPlan, result map[string]FieldBinding, values []PlanMetric) {
	for _, value := range values {
		addBindingValue(result, planFieldBinding(plan, value.NodeID, value.Column))
	}
}

func addOutputBindingValues(plan GraphPlan, result map[string]FieldBinding, values []PlanOutput) {
	for _, value := range values {
		addBindingValue(result, planFieldBinding(plan, value.NodeID, value.Column))
	}
}

func addBindingValue(result map[string]FieldBinding, value FieldBinding) {
	value = normalizeFieldBinding(value)
	if key := fieldBindingKey(value); key != "" {
		result[key] = value
	}
}

func filterGroupUses(values []FieldGroupUse, admitted map[string]bool) []FieldGroupUse {
	result := []FieldGroupUse{}
	for _, value := range values {
		if admitted[value.GroupID] {
			result = append(result, value)
		}
	}
	return result
}

func filterJoinUses(values []FieldJoinUse, admitted map[string]bool) []FieldJoinUse {
	result := []FieldJoinUse{}
	for _, value := range values {
		if admitted[value.JoinID] {
			result = append(result, value)
		}
	}
	return result
}

func symmetricJoinUseDifference(left, right []FieldJoinUse) []FieldJoinUse {
	leftSet := make(map[FieldJoinUse]bool, len(left))
	rightSet := make(map[FieldJoinUse]bool, len(right))
	for _, value := range left {
		leftSet[value] = true
	}
	for _, value := range right {
		rightSet[value] = true
	}
	result := []FieldJoinUse{}
	for value := range leftSet {
		if !rightSet[value] {
			result = append(result, value)
		}
	}
	for value := range rightSet {
		if !leftSet[value] {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return fieldJoinUseLess(result[i], result[j]) })
	return result
}

func containsFieldJoinUse(values []FieldJoinUse, expected FieldJoinUse) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func oppositeJoinSide(value string) string {
	if value == "LEFT" {
		return "RIGHT"
	}
	if value == "RIGHT" {
		return "LEFT"
	}
	return ""
}

func cloneFieldChanges(values []FieldChange) []FieldChange {
	if values == nil {
		return []FieldChange{}
	}
	result := make([]FieldChange, len(values))
	for index, value := range values {
		result[index] = value
		result[index].GroupUses = append([]FieldGroupUse(nil), value.GroupUses...)
		result[index].JoinUses = append([]FieldJoinUse(nil), value.JoinUses...)
		result[index].OutputUses = append([]FieldOutputUse(nil), value.OutputUses...)
		if result[index].GroupUses == nil {
			result[index].GroupUses = []FieldGroupUse{}
		}
		if result[index].JoinUses == nil {
			result[index].JoinUses = []FieldJoinUse{}
		}
		if result[index].OutputUses == nil {
			result[index].OutputUses = []FieldOutputUse{}
		}
	}
	return result
}

func canonicalFieldChanges(values []FieldChange) []FieldChange {
	result := cloneFieldChanges(values)
	for index := range result {
		value := &result[index]
		value.Field = normalizeFieldBinding(value.Field)
		value.SelectionAction = strings.ToUpper(strings.TrimSpace(value.SelectionAction))
		value.Purpose = strings.ToUpper(strings.TrimSpace(value.Purpose))
		for useIndex := range value.GroupUses {
			use := &value.GroupUses[useIndex]
			use.GroupID = strings.TrimSpace(use.GroupID)
			use.Role = strings.ToUpper(strings.TrimSpace(use.Role))
			use.Grouping = strings.ToUpper(strings.TrimSpace(use.Grouping))
			use.Aggregation = strings.ToUpper(strings.TrimSpace(use.Aggregation))
		}
		for useIndex := range value.JoinUses {
			use := &value.JoinUses[useIndex]
			use.JoinID = strings.TrimSpace(use.JoinID)
			use.Side = strings.ToUpper(strings.TrimSpace(use.Side))
			use.Peer = normalizeFieldBinding(use.Peer)
		}
		for useIndex := range value.OutputUses {
			use := &value.OutputUses[useIndex]
			use.EndID = strings.TrimSpace(use.EndID)
			use.Name = strings.TrimSpace(use.Name)
			use.Code = strings.TrimSpace(use.Code)
		}
		sort.Slice(value.GroupUses, func(i, j int) bool {
			if value.GroupUses[i].GroupID != value.GroupUses[j].GroupID {
				return value.GroupUses[i].GroupID < value.GroupUses[j].GroupID
			}
			if value.GroupUses[i].Role != value.GroupUses[j].Role {
				return value.GroupUses[i].Role < value.GroupUses[j].Role
			}
			if value.GroupUses[i].Grouping != value.GroupUses[j].Grouping {
				return value.GroupUses[i].Grouping < value.GroupUses[j].Grouping
			}
			return value.GroupUses[i].Aggregation < value.GroupUses[j].Aggregation
		})
		sort.Slice(value.JoinUses, func(i, j int) bool { return fieldJoinUseLess(value.JoinUses[i], value.JoinUses[j]) })
	}
	sort.Slice(result, func(i, j int) bool { return fieldBindingLess(result[i].Field, result[j].Field) })
	return result
}

func cloneGraphPlan(value GraphPlan) GraphPlan {
	result := value
	result.Nodes = append([]PlanNode(nil), value.Nodes...)
	for index := range result.Nodes {
		result.Nodes[index].SelectedColumns = append([]string(nil), value.Nodes[index].SelectedColumns...)
	}
	result.Joins = append([]PlanJoin(nil), value.Joins...)
	for index := range result.Joins {
		result.Joins[index].Conditions = append([]PlanJoinCondition(nil), value.Joins[index].Conditions...)
	}
	result.Groups = append([]PlanGroup(nil), value.Groups...)
	for index := range result.Groups {
		result.Groups[index].Dimensions = append([]PlanDimension(nil), value.Groups[index].Dimensions...)
		result.Groups[index].Metrics = append([]PlanMetric(nil), value.Groups[index].Metrics...)
	}
	result.Transforms = append([]PlanTransform(nil), value.Transforms...)
	for index := range result.Transforms {
		result.Transforms[index].Rules = append([]PlanTransformRule(nil), value.Transforms[index].Rules...)
		for ruleIndex := range result.Transforms[index].Rules {
			rule := &result.Transforms[index].Rules[ruleIndex]
			rule.InputKeys = append([]string(nil), value.Transforms[index].Rules[ruleIndex].InputKeys...)
			rule.ConditionValues = append([]PlanConditionValue(nil), value.Transforms[index].Rules[ruleIndex].ConditionValues...)
		}
	}
	result.End.Outputs = append([]PlanOutput(nil), value.End.Outputs...)
	return result
}

func appendUniqueString(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
