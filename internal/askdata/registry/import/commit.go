package registryimport

import (
	"context"
	"errors"
	"sort"
)

type CommitRepository interface {
	CommitRows(
		context.Context,
		string, string, string, string,
		CommitSelection,
		DraftCreator,
	) (State, []DraftReference, error)
}

type CommitInput struct {
	TenantID          string
	DomainID          string
	ActorID           string
	ImportID          string
	RowNumbers        []int
	All               bool
	AcknowledgeImpact bool
}

type CommitResult struct {
	ImportID  string           `json:"importId"`
	State     State            `json:"state"`
	Committed []DraftReference `json:"committed"`
}

type CommitService struct {
	store   CommitRepository
	creator DraftCreator
}

func NewCommitService(store CommitRepository, creator DraftCreator) *CommitService {
	return &CommitService{store: store, creator: creator}
}

func (service *CommitService) Commit(ctx context.Context, input CommitInput) (CommitResult, error) {
	if service == nil || service.store == nil || service.creator == nil ||
		!canonicalUUID(input.TenantID) || !canonicalUUID(input.DomainID) ||
		!canonicalUUID(input.ActorID) || !canonicalUUID(input.ImportID) ||
		input.All == (len(input.RowNumbers) > 0) || len(input.RowNumbers) > MaxImportRows {
		return CommitResult{}, ErrInvalidImport
	}
	numbers := append([]int(nil), input.RowNumbers...)
	sort.Ints(numbers)
	for index, value := range numbers {
		if value < 1 || index > 0 && value == numbers[index-1] {
			return CommitResult{}, ErrInvalidImport
		}
	}
	state, references, err := service.store.CommitRows(
		ctx, input.TenantID, input.DomainID, input.ActorID, input.ImportID,
		CommitSelection{RowNumbers: numbers, AcknowledgeImpact: input.AcknowledgeImpact},
		service.creator,
	)
	if err != nil {
		return CommitResult{}, err
	}
	if state != StateCommitted && state != StatePartiallyCommitted || len(references) == 0 {
		return CommitResult{}, errors.New("semantic import commit returned an invalid result")
	}
	return CommitResult{ImportID: input.ImportID, State: state, Committed: references}, nil
}
