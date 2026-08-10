package registryimport

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type commitRepositoryStub struct {
	selection CommitSelection
	state     State
	refs      []DraftReference
	err       error
}

func (stub *commitRepositoryStub) CommitRows(
	_ context.Context,
	_, _, _, _ string,
	selection CommitSelection,
	_ DraftCreator,
) (State, []DraftReference, error) {
	stub.selection = selection
	return stub.state, stub.refs, stub.err
}

type draftCreatorStub struct{}

func (draftCreatorStub) CreateDraft(
	context.Context, pgx.Tx, SemanticImport, ImportRow,
) (DraftReference, error) {
	return DraftReference{}, nil
}

func TestCommitServiceRequiresExactlyOneSelectionAndForwardsImpactAck(t *testing.T) {
	scope := func() CommitInput {
		return CommitInput{
			TenantID: uuid.NewString(), DomainID: uuid.NewString(),
			ActorID: uuid.NewString(), ImportID: uuid.NewString(),
		}
	}
	for name, mutate := range map[string]func(*CommitInput){
		"neither": func(*CommitInput) {},
		"both": func(input *CommitInput) {
			input.All, input.RowNumbers = true, []int{1}
		},
		"duplicate": func(input *CommitInput) {
			input.RowNumbers = []int{2, 1, 2}
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := scope()
			mutate(&input)
			service := NewCommitService(&commitRepositoryStub{}, draftCreatorStub{})
			if _, err := service.Commit(context.Background(), input); !errors.Is(err, ErrInvalidImport) {
				t.Fatalf("Commit() error = %v", err)
			}
		})
	}
	repository := &commitRepositoryStub{
		state: StatePartiallyCommitted,
		refs:  []DraftReference{{ObjectID: uuid.NewString(), VersionID: uuid.NewString(), Status: "DRAFT"}},
	}
	input := scope()
	input.RowNumbers = []int{9, 2}
	input.AcknowledgeImpact = true
	result, err := NewCommitService(repository, draftCreatorStub{}).Commit(context.Background(), input)
	if err != nil || result.State != StatePartiallyCommitted ||
		len(repository.selection.RowNumbers) != 2 || repository.selection.RowNumbers[0] != 2 ||
		!repository.selection.AcknowledgeImpact {
		t.Fatalf("Commit() = %#v, selection=%#v, err=%v", result, repository.selection, err)
	}
}

type withdrawRepositoryStub struct {
	reason   string
	rejected []WithdrawalRejection
	err      error
}

func (stub *withdrawRepositoryStub) WithdrawImportSelective(
	_ context.Context,
	_, _, _, _, reason string,
	_ SelectiveDraftWithdrawer,
) ([]WithdrawalRejection, error) {
	stub.reason = reason
	return stub.rejected, stub.err
}

type selectiveWithdrawerStub struct{}

func (selectiveWithdrawerStub) WithdrawDraftsSelective(
	context.Context, pgx.Tx, SemanticImport, []ImportRow,
) ([]WithdrawalRejection, error) {
	return nil, nil
}

func TestWithdrawServicePreservesRejectionList(t *testing.T) {
	repository := &withdrawRepositoryStub{rejected: []WithdrawalRejection{{
		RowNo: 4, VersionID: uuid.NewString(), Reason: "VERSION_REFERENCED",
	}}}
	input := WithdrawInput{
		TenantID: uuid.NewString(), DomainID: uuid.NewString(), ActorID: uuid.NewString(),
		ImportID: uuid.NewString(), Reason: "wrong source file",
	}
	result, err := NewWithdrawService(repository, selectiveWithdrawerStub{}).
		Withdraw(context.Background(), input)
	if err != nil || result.State != StateWithdrawn || len(result.Rejected) != 1 ||
		repository.reason != input.Reason {
		t.Fatalf("Withdraw() = %#v, reason=%q, err=%v", result, repository.reason, err)
	}
}
