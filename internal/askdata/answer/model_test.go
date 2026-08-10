package answer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
)

func TestAnswerArtifactRoundTripSchemaAndNormalization(t *testing.T) {
	artifact := validAnswerArtifact(t)
	raw, err := artifact.MarshalCanonical()
	if err != nil {
		t.Fatalf("MarshalCanonical() error = %v", err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, artifact.Normalize()) {
		t.Fatalf("round-trip artifact differs:\nactual: %#v\nwant: %#v", decoded, artifact.Normalize())
	}
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "schemas", "answer-artifact-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ai.ValidateStructuredOutput(ai.JSONSchema{Name: "answer_artifact_v1", Schema: schemaRaw}, raw); err != nil {
		t.Fatalf("schema validation error = %v", err)
	}
	if !strings.Contains(string(raw), `"textSpan":[`) {
		t.Fatalf("textSpan is not the frozen compact interval form: %s", raw)
	}
}

func TestAnswerArtifactRejectsUnknownFieldsAndInvalidCitationSpans(t *testing.T) {
	artifact := validAnswerArtifact(t)
	raw, err := artifact.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(string(raw), `"schemaVersion":"1.0"`, `"schemaVersion":"1.0","unexpected":true`, 1)
	if _, err := Decode([]byte(unknown)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}

	outOfBounds := artifact
	outOfBounds.Layers.Narrative.Citations = append([]shared.Citation(nil), artifact.Layers.Narrative.Citations...)
	outOfBounds.Layers.Narrative.Citations[2].TextSpan.End = 10_000
	if err := outOfBounds.Validate(); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("out-of-bounds span error = %v", err)
	}

	overlap := artifact
	overlap.Layers.Narrative.Citations = append([]shared.Citation(nil), artifact.Layers.Narrative.Citations...)
	overlap.Layers.Narrative.Citations[1].TextSpan = overlap.Layers.Narrative.Citations[0].TextSpan
	if err := overlap.Validate(); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlap error = %v", err)
	}
}

func TestAnswerArtifactStaleUsesOneSharedPredicate(t *testing.T) {
	artifact := validAnswerArtifact(t)
	current := artifact.StaleProvenance()
	if artifact.IsStale(current) {
		t.Fatal("identical Answer provenance is stale")
	}
	mutations := map[string]func(*shared.Provenance){
		"prompt":   func(value *shared.Provenance) { value.PromptVersion = "answer-v4" },
		"model":    func(value *shared.Provenance) { value.ModelPolicy = "narrative-strict" },
		"evidence": func(value *shared.Provenance) { value.EvidenceHash = askdata.HashBytes([]byte("new evidence")) },
		"result":   func(value *shared.Provenance) { value.ResultHash = askdata.HashBytes([]byte("new result")) },
		"release":  func(value *shared.Provenance) { value.SemanticReleaseID = "release:v2" },
		"chart":    func(value *shared.Provenance) { value.ChartRuleVersion = "1.1.0" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := current
			mutate(&changed)
			if !artifact.IsStale(changed) {
				t.Fatalf("%s change did not stale Answer Artifact", name)
			}
		})
	}
	changedVerifier := current
	changedVerifier.VerifierVersion = "1.1.0"
	if !artifact.IsStale(changedVerifier) {
		t.Fatal("verifier version change did not stale Answer Artifact")
	}
}

func TestDegradedAnswerRequiresEmptyNarrative(t *testing.T) {
	artifact := validAnswerArtifact(t)
	artifact.Layers.Narrative = NarrativeLayer{Findings: []string{}, Citations: []shared.Citation{}}
	artifact.Verification = Verification{
		VerifierVersion: "1.0.0", PolicyWordlistVersion: "1.0.0",
		Attempts: 2, Passed: false, Degraded: true,
	}
	if err := artifact.Validate(); err != nil {
		t.Fatalf("degraded Answer Artifact error = %v", err)
	}
	artifact.Layers.Narrative.Summary = "未经校验的结论"
	if err := artifact.Validate(); err == nil || !strings.Contains(err.Error(), "empty narrative") {
		t.Fatalf("non-empty degraded narrative error = %v", err)
	}
}

func TestAnswerArtifactStorageContractIsAppendOnly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000217_askdata_question_runtime_audit.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, expected := range []string{
		"'EVIDENCE','ANSWER','CLARIFICATION','BLOCK'",
		"CREATE TRIGGER askdata_question_artifacts_immutable",
		"BEFORE UPDATE OR DELETE ON askdata.question_artifacts",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("append-only storage contract missing %q", expected)
		}
	}
}

func validAnswerArtifact(t *testing.T) AnswerArtifact {
	t.Helper()
	rowKey, err := shared.FormatRowKey([]shared.RowKeyPart{{Key: "region", Value: "east"}, {Key: "month", Value: "2026-08"}})
	if err != nil {
		t.Fatal(err)
	}
	currency := "CNY"
	category := "month"
	summary := "2026年8月华东销售额为1280000元。"
	return AnswerArtifact{
		SchemaVersion: SchemaVersion,
		RunID:         askdata.ID(uuid.NewString()),
		Layers: AnswerLayers{
			Structured: StructuredLayer{
				Headline: &MetricValue{
					MetricVersionID: "metric:sales@v5", Value: "1280000.00", Unit: "CNY",
					Currency: &currency, Label: "销售额", ColumnKey: "sales_amount",
				},
				Cards: []MetricValue{},
				ChartSpec: &ChartSpec{
					Type: ChartLine, DatasetID: "result:monthly", CategoryColumnKey: &category,
					ValueColumnKeys: []string{"sales_amount"},
				},
				TableRef: "result:artifact:v1",
			},
			Narrative: NarrativeLayer{
				Summary: summary, Findings: []string{},
				Citations: []shared.Citation{
					shared.NewTimeSpecCitation(spanFor(t, summary, "2026年8月")),
					shared.NewContractCitation(spanFor(t, summary, "华东销售额"), "metric:sales@v5"),
					shared.NewResultCellCitation(spanFor(t, summary, "1280000元"), shared.CellRef{RowKey: rowKey, ColumnKey: "sales_amount"}),
				},
			},
		},
		Verification: Verification{
			VerifierVersion: "1.0.0", PolicyWordlistVersion: "1.0.0",
			Attempts: 1, Passed: true, Degraded: false,
		},
		Provenance: Provenance{
			PromptVersion: "answer-v3", ModelPolicy: "narrative-standard",
			EvidenceHash: askdata.HashBytes([]byte("evidence")), ResultHash: askdata.HashBytes([]byte("result")),
			SemanticReleaseID: "release:v1", ChartRuleVersion: "1.0.0",
		},
	}
}

func spanFor(t *testing.T, text, fragment string) shared.TextSpan {
	t.Helper()
	textRunes, fragmentRunes := []rune(text), []rune(fragment)
	for start := 0; start+len(fragmentRunes) <= len(textRunes); start++ {
		if string(textRunes[start:start+len(fragmentRunes)]) == fragment {
			return shared.TextSpan{Start: start, End: start + len(fragmentRunes)}
		}
	}
	t.Fatalf("fragment %q is not present in %q", fragment, text)
	return shared.TextSpan{}
}

func TestAnswerArtifactJSONContainsNoFloatMetricValues(t *testing.T) {
	artifact := validAnswerArtifact(t)
	raw, err := json.Marshal(artifact.Normalize())
	if err != nil {
		t.Fatal(err)
	}
	invalid := strings.Replace(string(raw), `"value":"1280000.00"`, `"value":1280000.00`, 1)
	if _, err := Decode([]byte(invalid)); err == nil {
		t.Fatal("numeric JSON value was accepted as an exact metric value")
	}
}
