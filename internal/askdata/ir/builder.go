package ir

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/binding"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

const (
	BuildArtifactVersion = "semantic-ir-build-v1"
	DefaultLimit         = DefaultTopN
	MaxBuildEvidence     = 256
)

var (
	ErrInvalidBuildRequest  = errors.New("semantic IR build request is invalid")
	ErrInvalidBuildArtifact = errors.New("semantic IR build artifact is invalid")
)

// InheritedTimeResolution restores the calendar boundaries that are
// intentionally absent from a QuestionUnderstanding context snapshot. It is
// accepted only when it is bound to the exact previous snapshot and its
// deterministic RULE evidence.
type InheritedTimeResolution struct {
	PreviousSnapshotHash askdata.ContentHash        `json:"previousSnapshotHash"`
	Resolved             understanding.ResolvedTime `json:"resolved"`
	Evidence             askdata.EvidenceRef        `json:"evidence"`
	ResolutionHash       askdata.ContentHash        `json:"resolutionHash"`
}

type BuildRequest struct {
	BindingRequest          binding.Request          `json:"bindingRequest"`
	BindingResult           binding.Result           `json:"bindingResult"`
	BundleHash              askdata.ContentHash      `json:"bundleHash"`
	InheritedTimeResolution *InheritedTimeResolution `json:"inheritedTimeResolution,omitempty"`
}

// BuildArtifact is the replayable boundary persisted as the SEMANTIC_IR
// artifact. SemanticIR remains the frozen compiler input; the surrounding
// hashes prove which authorized binding produced it.
type BuildArtifact struct {
	Version           string                `json:"version"`
	Scope             askdata.PolicyScope   `json:"scope"`
	DomainID          askdata.ID            `json:"domainId"`
	BindingResultHash askdata.ContentHash   `json:"bindingResultHash"`
	BundleHash        askdata.ContentHash   `json:"bundleHash"`
	EvidenceRefs      []askdata.EvidenceRef `json:"evidenceRefs"`
	IR                SemanticIR            `json:"ir"`
	IRHash            askdata.ContentHash   `json:"irHash"`
	ArtifactHash      askdata.ContentHash   `json:"artifactHash"`
}

// NewInheritedTimeResolution creates the only accepted proof shape for an
// inherited time slot. Callers must obtain resolved from the persisted prior
// turn rule artifact; this constructor does not parse or guess calendar text.
func NewInheritedTimeResolution(
	previousSnapshotHash askdata.ContentHash,
	resolved understanding.ResolvedTime,
) (InheritedTimeResolution, error) {
	if err := previousSnapshotHash.Validate(); err != nil {
		return InheritedTimeResolution{}, fmt.Errorf("previousSnapshotHash: %w", err)
	}
	payload, err := inheritedTimePayload(previousSnapshotHash, resolved)
	if err != nil {
		return InheritedTimeResolution{}, err
	}
	hash := askdata.HashBytes(payload)
	proof := InheritedTimeResolution{
		PreviousSnapshotHash: previousSnapshotHash,
		Resolved:             resolved,
		Evidence: askdata.EvidenceRef{
			EvidenceID:  askdata.ID("resolved-time:" + string(hash)),
			Kind:        askdata.EvidenceKindRule,
			SourceID:    askdata.ID("context-snapshot:" + string(previousSnapshotHash)),
			ContentHash: hash,
		},
		ResolutionHash: hash,
	}
	return proof, nil
}

func Build(request BuildRequest) (BuildArtifact, error) {
	selection, err := validateBuildRequest(request)
	if err != nil {
		return BuildArtifact{}, err
	}
	semanticIR, err := buildSemanticIR(request, selection)
	if err != nil {
		return BuildArtifact{}, err
	}
	normalizedIR, _, irHash, err := Canonicalize(semanticIR)
	if err != nil {
		return BuildArtifact{}, fmt.Errorf("%w: canonical IR: %v", ErrInvalidBuildRequest, err)
	}
	evidence := append([]askdata.EvidenceRef(nil), selection.Bundle.EvidenceRefs...)
	if request.InheritedTimeResolution != nil {
		evidence = append(evidence, request.InheritedTimeResolution.Evidence)
	}
	evidence = normalizeBuildEvidence(evidence)
	if len(evidence) == 0 || len(evidence) > MaxBuildEvidence {
		return BuildArtifact{}, fmt.Errorf("%w: evidence count", ErrInvalidBuildRequest)
	}
	artifact := BuildArtifact{
		Version: BuildArtifactVersion, Scope: request.BindingResult.Scope,
		DomainID: request.BindingResult.DomainID, BindingResultHash: request.BindingResult.ResultHash,
		BundleHash: request.BundleHash, EvidenceRefs: evidence, IR: normalizedIR, IRHash: irHash,
	}
	return finalizeBuildArtifact(artifact)
}

// DecodeBuildArtifact rejects unknown fields, then replays the binding and IR
// construction instead of trusting stored hashes in isolation.
func DecodeBuildArtifact(raw []byte, request BuildRequest) (BuildArtifact, error) {
	var artifact BuildArtifact
	if err := askdata.DecodeStrictJSON(raw, &artifact); err != nil {
		return BuildArtifact{}, err
	}
	if err := artifact.ValidateAgainst(request); err != nil {
		return BuildArtifact{}, err
	}
	return artifact, nil
}

func (artifact BuildArtifact) ValidateAgainst(request BuildRequest) error {
	expected, err := Build(request)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(artifact, expected) {
		return ErrInvalidBuildArtifact
	}
	return nil
}

func finalizeBuildArtifact(artifact BuildArtifact) (BuildArtifact, error) {
	artifact.ArtifactHash = ""
	payload, err := json.Marshal(artifact)
	if err != nil {
		return BuildArtifact{}, fmt.Errorf("marshal build artifact: %w", err)
	}
	artifact.ArtifactHash = askdata.HashBytes(payload)
	return artifact, nil
}

func normalizeBuildEvidence(values []askdata.EvidenceRef) []askdata.EvidenceRef {
	result := append([]askdata.EvidenceRef(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].EvidenceID != result[j].EvidenceID {
			return result[i].EvidenceID < result[j].EvidenceID
		}
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		if result[i].SourceID != result[j].SourceID {
			return result[i].SourceID < result[j].SourceID
		}
		return result[i].ContentHash < result[j].ContentHash
	})
	write := 0
	for _, value := range result {
		if write > 0 && value == result[write-1] {
			continue
		}
		result[write] = value
		write++
	}
	return result[:write]
}

func inheritedTimePayload(
	previousSnapshotHash askdata.ContentHash,
	resolved understanding.ResolvedTime,
) ([]byte, error) {
	return registry.CanonicalValue(struct {
		Version              string                     `json:"version"`
		PreviousSnapshotHash askdata.ContentHash        `json:"previousSnapshotHash"`
		Resolved             understanding.ResolvedTime `json:"resolved"`
	}{"inherited-time-resolution-v1", previousSnapshotHash, resolved})
}
