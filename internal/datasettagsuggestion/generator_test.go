package datasettagsuggestion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	aiplatform "intelligent-report-generation-system/internal/ai"
)

type sequenceInvoker struct {
	results     []aiplatform.InvocationResult
	errors      []error
	invocations []aiplatform.Invocation
}

func (invoker *sequenceInvoker) Configured() bool { return true }

func (invoker *sequenceInvoker) Invoke(
	_ context.Context,
	invocation aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	invoker.invocations = append(invoker.invocations, invocation)
	index := len(invoker.invocations) - 1
	var result aiplatform.InvocationResult
	if index < len(invoker.results) {
		result = invoker.results[index]
	}
	if index < len(invoker.errors) {
		return result, invoker.errors[index]
	}
	return result, nil
}

func TestGenerateRepairsInvalidControlledTagOutput(t *testing.T) {
	tagID := uuid.NewString()
	firstRequestID := uuid.NewString()
	repairRequestID := uuid.NewString()
	invoker := &sequenceInvoker{results: []aiplatform.InvocationResult{
		{
			RequestID: firstRequestID,
			ProviderResult: aiplatform.ProviderResult{Content: []byte(
				`{"items":[{"tagId":"` + tagID + `","confidence":0.8,"rationale":"a"},{"tagId":"` + tagID + `","confidence":0.7,"rationale":"b"}]}`,
			)},
		},
		{
			RequestID: repairRequestID,
			ProviderResult: aiplatform.ProviderResult{Content: []byte(
				`{"items":[{"tagId":"` + tagID + `","confidence":0.8,"rationale":"evidence"}]}`,
			)},
		},
	}}
	generator := NewGenerator(invoker, time.Second)
	completion, err := generator.Generate(context.Background(), validTestClaim(), Input{
		Taxonomy: []TaxonomyTag{{
			ID: tagID, Code: "CUSTOMER", Name: "客户",
			Category: "BUSINESS_ENTITY",
		}},
	})
	if err != nil {
		t.Fatalf("generate repaired output: %v", err)
	}
	if completion.AIRequestID != repairRequestID || len(completion.Suggestions) != 1 {
		t.Fatalf("unexpected completion: %#v", completion)
	}
	if len(invoker.invocations) != 2 {
		t.Fatalf("invocation count = %d", len(invoker.invocations))
	}
	messages := invoker.invocations[1].Request.Messages
	if len(messages) != 4 || messages[2].Role != aiplatform.MessageRoleAssistant ||
		!strings.Contains(messages[3].Parts[0].Text, "结构诊断") {
		t.Fatalf("repair messages = %#v", messages)
	}
}

func TestGenerateReturnsRetryableInvalidOutputAfterRepairFailure(t *testing.T) {
	tagID := uuid.NewString()
	invalid := aiplatform.InvocationResult{
		RequestID: uuid.NewString(),
		ProviderResult: aiplatform.ProviderResult{Content: []byte(
			`{"items":[{"tagId":"` + tagID + `","confidence":2,"rationale":"invalid"}]}`,
		)},
	}
	invoker := &sequenceInvoker{results: []aiplatform.InvocationResult{invalid, invalid}}
	generator := NewGenerator(invoker, time.Second)
	_, err := generator.Generate(context.Background(), validTestClaim(), Input{
		Taxonomy: []TaxonomyTag{{
			ID: tagID, Code: "CUSTOMER", Name: "客户",
			Category: "BUSINESS_ENTITY",
		}},
	})
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("expected invalid output, got %v", err)
	}
}

func validTestClaim() Claim {
	return Claim{
		ID: uuid.NewString(), TenantID: uuid.NewString(),
		DatasetID: uuid.NewString(), DatasetVersionID: uuid.NewString(),
		SchemaHash: strings.Repeat("a", 64), Layer: "DIM",
		PromptVersion: PromptVersion, ActorID: uuid.NewString(),
		LeaseToken: uuid.NewString(), Attempt: 1, MaxAttempts: 3,
	}
}
