package insight

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
)

func TestEvidenceBundleBothSourcesRoundTripAndSchema(t *testing.T) {
	for _, source := range []SourceType{SourceSemanticIR, SourceDatasetQuery} {
		t.Run(string(source), func(t *testing.T) {
			bundle := validEvidenceBundle(t, source)
			raw, err := json.Marshal(bundle.Normalize())
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeEvidenceBundle(raw)
			if err != nil {
				t.Fatalf("DecodeEvidenceBundle() error = %v", err)
			}
			if !reflect.DeepEqual(decoded, bundle.Normalize()) {
				t.Fatalf("round-trip bundle differs:\nactual: %#v\nwant: %#v", decoded, bundle.Normalize())
			}
			validateSchema(t, "evidence_bundle_v1", "evidence-bundle-v1.schema.json", raw)
		})
	}
}

func TestEvidenceBundleEnforcesSourceSpecificSemanticFields(t *testing.T) {
	semantic := validEvidenceBundle(t, SourceSemanticIR)
	semantic.SemanticReleaseID = nil
	if err := semantic.Validate(); err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("SEMANTIC_IR missing release error = %v", err)
	}
	dataset := validEvidenceBundle(t, SourceDatasetQuery)
	release := askdata.ID("release:v1")
	dataset.SemanticReleaseID = &release
	if err := dataset.Validate(); err == nil || !strings.Contains(err.Error(), "null") {
		t.Fatalf("DATASET_QUERY semantic release error = %v", err)
	}
}

func TestInsightArtifactRoundTripSchemaAndEvidenceBinding(t *testing.T) {
	bundle := validEvidenceBundle(t, SourceSemanticIR)
	artifact := validInsightArtifact(t, bundle)
	raw, err := json.Marshal(artifact.Normalize())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeInsightArtifact(raw)
	if err != nil {
		t.Fatalf("DecodeInsightArtifact() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, artifact.Normalize()) {
		t.Fatalf("round-trip artifact differs:\nactual: %#v\nwant: %#v", decoded, artifact.Normalize())
	}
	if err := decoded.ValidateAgainst(bundle); err != nil {
		t.Fatalf("ValidateAgainst() error = %v", err)
	}
	validateSchema(t, "insight_artifact_v1", "insight-artifact-v1.schema.json", raw)

	wrong := bundle
	wrong.DataSnapshotVersion = "snap-2026-08-07-01"
	if err := decoded.ValidateAgainst(wrong); err == nil || !strings.Contains(err.Error(), "evidenceHash") {
		t.Fatalf("changed Evidence Bundle error = %v", err)
	}
}

func TestSharedIsStaleCoversNineRequiredInsightFactors(t *testing.T) {
	bundle := validEvidenceBundle(t, SourceSemanticIR)
	artifact := validInsightArtifact(t, bundle)
	current := artifact.StaleProvenance(bundle)
	if artifact.IsStale(bundle, current) {
		t.Fatal("identical Insight provenance is stale")
	}
	mutations := map[string]func(*shared.Provenance){
		"datasetVersion":           func(value *shared.Provenance) { value.DatasetVersionID = "dataset:v2" },
		"dataSnapshotVersion":      func(value *shared.Provenance) { value.DataSnapshotVersion = "snap-2026-08-07-01" },
		"queryHash":                func(value *shared.Provenance) { value.QueryHash = askdata.HashBytes([]byte("query v2")) },
		"filterHash":               func(value *shared.Provenance) { value.FilterHash = askdata.HashBytes([]byte("filter v2")) },
		"analysisMethodVersion":    func(value *shared.Provenance) { value.AnalysisMethodVersion = "1.3.0" },
		"evidenceAlgorithmVersion": func(value *shared.Provenance) { value.EvidenceAlgorithmVersion = "1.2.0" },
		"promptVersion":            func(value *shared.Provenance) { value.PromptVersion = "insight-monthly-v3" },
		"modelPolicy":              func(value *shared.Provenance) { value.ModelPolicy = "narrative-strict" },
		"verifierVersion":          func(value *shared.Provenance) { value.VerifierVersion = "1.1.0" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := current
			mutate(&changed)
			if !artifact.IsStale(bundle, changed) {
				t.Fatalf("%s change did not stale Insight Artifact", name)
			}
		})
	}
	changedWordlist := current
	changedWordlist.PolicyWordlistVersion = "1.1.0"
	if !artifact.IsStale(bundle, changedWordlist) {
		t.Fatal("policy wordlist change did not stale Insight Artifact")
	}
}

func TestEvidenceAndAnswerCoordinatesAreInteroperable(t *testing.T) {
	bundle := validEvidenceBundle(t, SourceSemanticIR)
	ref := bundle.Facts[0].CellRefs[0]
	citation := shared.NewResultCellCitation(shared.TextSpan{Start: 0, End: 4}, ref)
	parsed, ok := citation.CellRef()
	if !ok || parsed != ref {
		t.Fatalf("citation CellRef() = %#v, %t, want %#v", parsed, ok, ref)
	}
	parts, err := shared.ParseRowKey(parsed.RowKey)
	if err != nil || len(parts) != 2 || parts[0].Key != "region" || parts[1].Key != "month" {
		t.Fatalf("shared row coordinate = %#v, %v", parts, err)
	}
}

func TestDecimalFieldsRejectJSONNumbersAndNonCanonicalStrings(t *testing.T) {
	bundle := validEvidenceBundle(t, SourceSemanticIR)
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	floatJSON := strings.Replace(string(raw), `"currentValue":"1280000.00"`, `"currentValue":1280000.00`, 1)
	if _, err := DecodeEvidenceBundle([]byte(floatJSON)); err == nil {
		t.Fatal("JSON float was accepted for currentValue")
	}
	nonCanonical := bundle
	nonCanonical.Facts[0].CurrentValue = "1.28e6"
	if err := nonCanonical.Validate(); err == nil || !strings.Contains(err.Error(), "decimal string") {
		t.Fatalf("scientific decimal error = %v", err)
	}
}

func TestHumanEditedInsightRequiresEditorAndTimestamp(t *testing.T) {
	bundle := validEvidenceBundle(t, SourceSemanticIR)
	artifact := validInsightArtifact(t, bundle)
	artifact.HumanEdited = true
	artifact.Citations = []shared.Citation{}
	if err := artifact.Validate(); err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("missing human editor error = %v", err)
	}
	editor := askdata.ID("user:analyst")
	editedAt := "2026-08-06T03:00:00+08:00"
	artifact.HumanEditedBy, artifact.HumanEditedAt = &editor, &editedAt
	if err := artifact.Validate(); err != nil {
		t.Fatalf("human-edited Insight error = %v", err)
	}
	artifact.HumanEdited = false
	if err := artifact.Validate(); err == nil || !strings.Contains(err.Error(), "null") {
		t.Fatalf("unexpected editor metadata error = %v", err)
	}
}

func TestInsightContractsRejectUnknownFields(t *testing.T) {
	bundle := validEvidenceBundle(t, SourceSemanticIR)
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(string(raw), `"schemaVersion":"1.1"`, `"schemaVersion":"1.1","sql":"SELECT 1"`, 1)
	if _, err := DecodeEvidenceBundle([]byte(unknown)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
}

func validEvidenceBundle(t *testing.T, source SourceType) EvidenceBundle {
	t.Helper()
	rowKey, err := shared.FormatRowKey([]shared.RowKeyPart{{Key: "region", Value: "east"}, {Key: "month", Value: "2026-08"}})
	if err != nil {
		t.Fatal(err)
	}
	previous, rate := "1100000.00", "0.1636"
	bundle := EvidenceBundle{
		SchemaVersion: EvidenceSchemaVersion, SourceType: source,
		DatasetVersionID: "dataset:v1", DataSnapshotVersion: "snap-2026-08-06-01",
		QueryPlanHash: askdata.HashBytes([]byte("query plan")), FilterHash: askdata.HashBytes([]byte("filter")),
		AsOf: "2026-08-06T02:10:00+08:00",
		ResolvedTimeRange: ResolvedTimeRange{
			Start: "2026-08-01T00:00:00+08:00", EndExclusive: "2026-08-07T00:00:00+08:00", Timezone: "Asia/Shanghai",
		},
		AnalysisMethod: AnalysisPeriodComparison, AnalysisMethodVersion: "1.2.0",
		EvidenceAlgorithmVersion: "1.1.0",
		Facts: []Fact{{
			ID: "fact_sales_growth", MetricVersionID: "metric:sales@v5",
			CurrentValue: "1280000.00", PreviousValue: &previous, ChangeRate: &rate, Unit: "CNY",
			CellRefs: []shared.CellRef{{RowKey: rowKey, ColumnKey: "sales_amount"}},
		}},
		QualityWarnings: []QualityWarning{}, GeneratedAt: "2026-08-06T02:11:00+08:00",
	}
	if source == SourceSemanticIR {
		release := askdata.ID("release:v1")
		hash := askdata.HashBytes([]byte("semantic ir"))
		bundle.SemanticReleaseID, bundle.SemanticIRHash = &release, &hash
	}
	return bundle
}

func validInsightArtifact(t *testing.T, bundle EvidenceBundle) InsightArtifact {
	t.Helper()
	hash, err := bundle.Hash()
	if err != nil {
		t.Fatal(err)
	}
	summary := "销售额较上期增长16.36%。"
	return InsightArtifact{
		SchemaVersion: InsightSchemaVersion, ID: "insight_sales_summary", EvidenceHash: hash,
		PromptVersion: "insight-monthly-v2", ModelPolicy: "narrative-standard",
		VerifierVersion: "1.0.0", PolicyWordlistVersion: "1.0.0",
		Content: InsightContent{Summary: summary, Findings: []string{}, Risks: []string{}, Actions: []string{}},
		Citations: []shared.Citation{
			shared.NewResultCellCitation(insightSpanFor(t, summary, "16.36%"), bundle.Facts[0].CellRefs[0]),
		},
		Status: InsightCurrent, HumanEdited: false, HumanEditedBy: nil, HumanEditedAt: nil,
	}
}

func insightSpanFor(t *testing.T, text, fragment string) shared.TextSpan {
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

func validateSchema(t *testing.T, name, filename string, raw []byte) {
	t.Helper()
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "schemas", filename))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ai.ValidateStructuredOutput(ai.JSONSchema{Name: name, Schema: schemaRaw}, raw); err != nil {
		t.Fatalf("%s validation error = %v", filename, err)
	}
}
