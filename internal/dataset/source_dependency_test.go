package dataset

import (
	"context"
	"encoding/json"
	"testing"
)

type sourceDependencyResolverStub map[string]Document

func (stub sourceDependencyResolverStub) ResolveDatasetVersionDocument(
	_ context.Context,
	versionID string,
) (Document, error) {
	return stub[versionID], nil
}

func TestPrepareAllowsCrossSourceLogicalDatasetNodes(t *testing.T) {
	document := validLogicalCrossSourceDWD()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(raw)
	if err != nil {
		if validation, ok := err.(*ValidationError); ok {
			t.Fatalf("logical CROSS_SOURCE validation: %#v", validation.Issues)
		}
		t.Fatalf("logical CROSS_SOURCE validation: %v", err)
	}
	if prepared.Document.Dataset.Type != "CROSS_SOURCE" {
		t.Fatalf(
			"dataset type = %q, want CROSS_SOURCE",
			prepared.Document.Dataset.Type,
		)
	}
}

func TestValidateSourceDependenciesResolvesODSBelowDIM(t *testing.T) {
	document := validLogicalCrossSourceDWD()
	resolver := sourceDependencyResolverStub{
		"fact_version": {
			Nodes: []Node{{
				ID: "fact_source", Type: "TABLE",
				DataSourceID: "oracle_source",
			}},
		},
		"dimension_version": {
			Nodes: []Node{{
				ID: "dimension_source", Type: "DATASET",
				DatasetVersionID: "dimension_ods_version",
			}},
		},
		"dimension_ods_version": {
			Nodes: []Node{{
				ID: "dimension_table", Type: "TABLE",
				DataSourceID: "mysql_source",
			}},
		},
	}
	if err := ValidateSourceDependencies(
		context.Background(), document, resolver,
	); err != nil {
		t.Fatalf("resolve logical physical sources: %v", err)
	}

	document.Dataset.Type = "SINGLE_SOURCE"
	err := ValidateSourceDependencies(
		context.Background(), document, resolver,
	)
	if err == nil {
		t.Fatal("logical SINGLE_SOURCE accepted two physical sources")
	}
	validation, ok := err.(*ValidationError)
	if !ok || len(validation.Issues) != 1 ||
		validation.Issues[0].Path != "dataset.type" {
		t.Fatalf("unexpected source dependency error: %#v", err)
	}
}

func TestValidateSourceDependenciesRejectsFalseCrossSource(t *testing.T) {
	document := validLogicalCrossSourceDWD()
	resolver := sourceDependencyResolverStub{
		"fact_version": {
			Nodes: []Node{{
				ID: "fact_source", Type: "TABLE",
				DataSourceID: "shared_source",
			}},
		},
		"dimension_version": {
			Nodes: []Node{{
				ID: "dimension_source", Type: "TABLE",
				DataSourceID: "shared_source",
			}},
		},
	}
	if err := ValidateSourceDependencies(
		context.Background(), document, resolver,
	); err == nil {
		t.Fatal("logical CROSS_SOURCE accepted one physical source")
	}
}

func validLogicalCrossSourceDWD() Document {
	return Document{
		DSLVersion: DSLVersion,
		Dataset: Descriptor{
			Code: "dwd_cross_source_test", Name: "跨源事实测试表",
			Type: "CROSS_SOURCE", Layer: LayerDWD,
		},
		Nodes: []Node{
			{
				ID: "fact", Type: "DATASET",
				DatasetVersionID: "fact_version", Alias: "fact",
				Projection:    []string{"fact_id", "dimension_id"},
				SourceFilters: []SourceFilter{},
			},
			{
				ID: "dimension", Type: "DATASET",
				DatasetVersionID: "dimension_version", Alias: "dimension",
				Projection:    []string{"dimension_id", "dimension_name"},
				SourceFilters: []SourceFilter{},
			},
		},
		Joins: []Join{{
			ID: "join_dimension", LeftNodeID: "fact",
			RightNodeID: "dimension", JoinType: "LEFT",
			Cardinality: "MANY_TO_ONE", ManualConfirmed: true,
			Conditions: []JoinCondition{{
				Operator: "EQUALS",
				LeftExpression: Expression{
					Type: "FIELD_REF", NodeID: "fact",
					Field: "dimension_id",
				},
				RightExpression: Expression{
					Type: "FIELD_REF", NodeID: "dimension",
					Field: "dimension_id",
				},
			}},
		}},
		PreAggregations: []PreAggregation{},
		Fields: []Field{
			{
				ID: "field_fact_id", Code: "fact_id", Name: "事实ID",
				Role: "IDENTIFIER", CanonicalType: "INTEGER",
				Expression: Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: "fact_id",
				},
			},
			{
				ID: "field_dimension_name", Code: "dimension_name",
				Name: "维度名称", Role: "ATTRIBUTE", CanonicalType: "STRING",
				Expression: Expression{
					Type: "FIELD_REF", NodeID: "dimension",
					Field: "dimension_name",
				},
			},
		},
		Filters: []Filter{}, GroupBy: []string{}, Having: []Filter{},
		Sorts: []Sort{}, Parameters: []Parameter{},
		OutputGrain: OutputGrain{
			Description: "每行代表一条跨源事实",
			KeyFields:   []string{"fact_id"},
		},
		ExecutionPolicy: ExecutionPolicy{
			Mode: "REALTIME", TimeoutMS: 1000,
			PreviewLimit: 100, ResultLimit: 1000,
		},
	}
}
