package compiler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const PlanJoinBlockedCode = "PLAN_JOIN_BLOCKED"

var (
	ErrInvalidJoinContract = errors.New("semantic join contract is invalid")
	ErrJoinBlocked         = errors.New("semantic join is blocked")
	joinIdentifierPattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,62}$`)
)

type JoinBehavior string

const (
	JoinDirect        JoinBehavior = "DIRECT"
	JoinPreAggregate  JoinBehavior = "PRE_AGGREGATE_RIGHT"
	JoinThroughBridge JoinBehavior = "BRIDGE_PRE_AGGREGATE_DEDUP"
)

type JoinSource struct {
	ModelVersionID askdata.ID
	Schema         string
	Relation       string
	Alias          string
	GroupBy        []string
	Measures       []JoinMeasure
}

type JoinMeasure struct {
	Column   string
	Function registry.Aggregation
	Alias    string
}

type BridgeJoinSpec struct {
	Source            JoinSource
	LeftSourceColumn  string
	LeftBridgeColumn  string
	RightBridgeColumn string
	RightSourceColumn string
}

type JoinCompileRequest struct {
	Relationship RelationshipContract
	Left         JoinSource
	Right        JoinSource
	Bridge       *BridgeJoinSpec
}

type CompiledJoin struct {
	SQL      string             `json:"sql"`
	Behavior JoinBehavior       `json:"behavior"`
	RiskCode graph.JoinRiskCode `json:"riskCode"`
}

type relationshipJoinAST struct {
	Type         string `json:"type"`
	LeftFieldID  string `json:"leftFieldId"`
	RightFieldID string `json:"rightFieldId"`
}

// CompileJoin implements the complete cardinality/fanout matrix. It accepts
// only release-pinned RelationshipContract and identifier-only source facts;
// values and arbitrary SQL never cross this boundary.
func CompileJoin(request JoinCompileRequest) (CompiledJoin, error) {
	relationship := request.Relationship
	if relationship.Cardinality == "" || relationship.FanoutPolicy == "" ||
		relationship.FanoutPolicy == registry.FanoutBlock {
		return CompiledJoin{}, fmt.Errorf("%s: %w", PlanJoinBlockedCode, ErrJoinBlocked)
	}
	if err := validateJoinCompileRequest(request); err != nil {
		return CompiledJoin{}, err
	}
	riskCode := graph.JoinRiskCode(
		string(relationship.Cardinality) + "_" + string(relationship.FanoutPolicy),
	)
	switch {
	case (relationship.Cardinality == registry.CardinalityOneToOne ||
		relationship.Cardinality == registry.CardinalityManyToOne) &&
		relationship.FanoutPolicy == registry.FanoutSafe:
		sql, err := compileDirectJoin(request)
		if err != nil {
			return CompiledJoin{}, err
		}
		return CompiledJoin{SQL: sql, Behavior: JoinDirect, RiskCode: riskCode}, nil
	case relationship.Cardinality == registry.CardinalityOneToMany &&
		relationship.FanoutPolicy == registry.FanoutPreAggregateRequired:
		sql, err := compilePreAggregateJoin(request)
		if err != nil {
			return CompiledJoin{}, err
		}
		return CompiledJoin{SQL: sql, Behavior: JoinPreAggregate, RiskCode: riskCode}, nil
	case relationship.Cardinality == registry.CardinalityManyToMany &&
		relationship.FanoutPolicy == registry.FanoutBridgeRequired:
		sql, err := compileBridgeJoin(request)
		if err != nil {
			return CompiledJoin{}, err
		}
		return CompiledJoin{SQL: sql, Behavior: JoinThroughBridge, RiskCode: riskCode}, nil
	default:
		return CompiledJoin{}, ErrInvalidJoinContract
	}
}

func validateJoinCompileRequest(request JoinCompileRequest) error {
	relationship := request.Relationship
	bridgeID := string(relationship.BridgeModelVersionID)
	if relationship.RelationshipVersionID.Validate() != nil ||
		relationship.LeftModelVersionID.Validate() != nil ||
		relationship.RightModelVersionID.Validate() != nil ||
		!validJoinType(relationship.JoinType) ||
		registry.ValidateRelationshipCombination(
			relationship.Cardinality, relationship.FanoutPolicy, bridgeID,
		) != nil || validateJoinSource(request.Left) != nil ||
		validateJoinSource(request.Right) != nil ||
		request.Left.ModelVersionID != relationship.LeftModelVersionID ||
		request.Right.ModelVersionID != relationship.RightModelVersionID {
		return ErrInvalidJoinContract
	}
	if _, err := decodeRelationshipJoinAST(relationship.JoinAST); err != nil {
		return err
	}
	if relationship.FanoutPolicy == registry.FanoutBridgeRequired {
		if request.Bridge == nil || validateJoinSource(request.Bridge.Source) != nil ||
			request.Bridge.Source.ModelVersionID != relationship.BridgeModelVersionID ||
			len(request.Bridge.Source.GroupBy) != 0 || len(request.Bridge.Source.Measures) != 0 ||
			!validJoinIdentifier(request.Bridge.LeftSourceColumn) ||
			!validJoinIdentifier(request.Bridge.LeftBridgeColumn) ||
			!validJoinIdentifier(request.Bridge.RightBridgeColumn) ||
			!validJoinIdentifier(request.Bridge.RightSourceColumn) {
			return ErrInvalidJoinContract
		}
	} else if request.Bridge != nil {
		return ErrInvalidJoinContract
	}
	return nil
}

func validateJoinSource(source JoinSource) error {
	if source.ModelVersionID.Validate() != nil || !validJoinIdentifier(source.Schema) ||
		!validJoinIdentifier(source.Relation) || !validJoinIdentifier(source.Alias) {
		return ErrInvalidJoinContract
	}
	seen := map[string]bool{}
	for _, column := range source.GroupBy {
		if !validJoinIdentifier(column) || seen[column] {
			return ErrInvalidJoinContract
		}
		seen[column] = true
	}
	for _, measure := range source.Measures {
		if !validJoinIdentifier(measure.Column) || !validJoinIdentifier(measure.Alias) ||
			seen[measure.Alias] || !validJoinAggregation(measure.Function) {
			return ErrInvalidJoinContract
		}
		seen[measure.Alias] = true
	}
	return nil
}

func validJoinAggregation(value registry.Aggregation) bool {
	switch value {
	case registry.AggregationSum, registry.AggregationAverage, registry.AggregationMinimum,
		registry.AggregationMaximum, registry.AggregationCount,
		registry.AggregationCountDistinct:
		return true
	default:
		return false
	}
}

func decodeRelationshipJoinAST(raw json.RawMessage) (relationshipJoinAST, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var contract relationshipJoinAST
	if err := decoder.Decode(&contract); err != nil || contract.Type != "EQUALS" ||
		!validJoinIdentifier(contract.LeftFieldID) ||
		!validJoinIdentifier(contract.RightFieldID) {
		return relationshipJoinAST{}, ErrInvalidJoinContract
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return relationshipJoinAST{}, ErrInvalidJoinContract
	}
	return contract, nil
}

func compileDirectJoin(request JoinCompileRequest) (string, error) {
	condition, _ := decodeRelationshipJoinAST(request.Relationship.JoinAST)
	return "SELECT * FROM " + sourceRelation(request.Left) + " " +
		joinKeyword(request.Relationship.JoinType) + " " + sourceRelation(request.Right) +
		" ON " + qualified(request.Left.Alias, condition.LeftFieldID) + " = " +
		qualified(request.Right.Alias, condition.RightFieldID), nil
}

func sourceRelation(source JoinSource) string {
	return quoteJoinIdentifier(source.Schema) + "." + quoteJoinIdentifier(source.Relation) +
		" AS " + quoteJoinIdentifier(source.Alias)
}

func joinKeyword(joinType registry.JoinType) string {
	return string(joinType) + " JOIN"
}

func qualified(alias, column string) string {
	return quoteJoinIdentifier(alias) + "." + quoteJoinIdentifier(column)
}

func quoteJoinIdentifier(value string) string {
	return `"` + value + `"`
}

func validJoinIdentifier(value string) bool {
	return joinIdentifierPattern.MatchString(value)
}

func orderedUnique(values ...[]string) []string {
	seen := make(map[string]bool)
	result := []string{}
	for _, group := range values {
		for _, value := range group {
			if !seen[value] {
				seen[value] = true
				result = append(result, value)
			}
		}
	}
	return result
}

func comma(values []string) string { return strings.Join(values, ", ") }
