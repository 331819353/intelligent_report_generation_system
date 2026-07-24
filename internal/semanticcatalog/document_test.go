package semanticcatalog

import (
	"fmt"
	"strings"
	"testing"
)

func TestDocumentIsDeterministicBoundedAndSanitized(t *testing.T) {
	facts := documentFacts{
		Subject: Subject{Type: SubjectDimensionMember, DimensionMemberID: testDocument},
		Lines: []documentLine{
			{Label: "成员键", Value: "690\n"},
			{Label: "规范名称", Value: "智家\t生态圈"},
		},
		Tags: []string{"组织", "690", "组织", "智家生态圈"},
	}
	first, firstHash, err := buildDocument(facts)
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := buildDocument(facts)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || firstHash != secondHash || len(firstHash) != 64 {
		t.Fatal("semantic document or input hash is not deterministic")
	}
	if strings.Contains(first, "\t") || strings.Contains(first, "690\n\n") {
		t.Fatalf("control characters were not normalized: %q", first)
	}
	if !strings.Contains(first, "成员键：690") ||
		!strings.Contains(first, "规范名称：智家 生态圈") ||
		!strings.Contains(first, "检索标签：690、智家生态圈、组织") {
		t.Fatalf("dimension member aliases are not searchable: %q", first)
	}
}

func TestUnlimitedTagsProduceBoundedDocumentAndTailEditsChangeHash(t *testing.T) {
	tags := make([]string, 20000)
	for index := range tags {
		tags[index] = fmt.Sprintf("业务标签-%05d-%s", index, strings.Repeat("x", 24))
	}
	facts := documentFacts{
		Subject: Subject{Type: SubjectDatasetVersion, DatasetVersionID: testDocument},
		Lines:   []documentLine{{Label: "数据集名称", Value: "销售宽表"}},
		Tags:    tags,
	}
	document, originalHash, err := buildDocument(facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(document) > maxDocumentBytes || !strings.Contains(document, "完整事实摘要") {
		t.Fatalf("large tag taxonomy was not represented safely: bytes=%d", len(document))
	}
	facts.Tags[len(facts.Tags)-1] = "只修改被截断尾部的标签"
	_, editedHash, err := buildDocument(facts)
	if err != nil {
		t.Fatal(err)
	}
	if editedHash == originalHash {
		t.Fatal("an edit to a truncated tail tag did not force re-embedding")
	}
}

func TestDatasetFieldReferenceKeepsExactLogicalField(t *testing.T) {
	claim := Claim{
		SubjectType: SubjectDatasetField,
		SubjectRef:  testDocument + ":customer:code",
	}
	subject, err := subjectFromClaim(claim)
	if err != nil {
		t.Fatal(err)
	}
	if subject.DatasetVersionID != testDocument || subject.DatasetFieldID != "customer:code" {
		t.Fatalf("unexpected parsed subject: %+v", subject)
	}
}
