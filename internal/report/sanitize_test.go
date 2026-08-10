package report

import (
	"bytes"
	"strings"
	"testing"
)

func TestSanitizeRichTextUsesDeterministicTagAndAttributeAllowlist(t *testing.T) {
	input := `<p class="drop" onclick="x">正文<custom>保留</custom><strong>重点</strong>` +
		`<script>secret</script><a target="_blank" href="https://example.invalid" style="x">链接</a>` +
		`<a href="data:text/html,bad">坏链</a></p>`
	got := SanitizeRichText(input)
	if got != SanitizeRichText(got) {
		t.Fatalf("sanitizer is not idempotent: %s", got)
	}
	for _, forbidden := range []string{"class=", "onclick", "custom", "script", "secret", "style=", "data:"} {
		if strings.Contains(strings.ToLower(got), forbidden) {
			t.Fatalf("sanitizer retained %q: %s", forbidden, got)
		}
	}
	for _, allowed := range []string{"正文保留", "<strong>重点</strong>", `href="https://example.invalid"`, `rel="noopener noreferrer"`} {
		if !strings.Contains(got, allowed) {
			t.Fatalf("sanitizer lost %q: %s", allowed, got)
		}
	}
}

func TestDecodeSanitizesRichTextBeforeValidation(t *testing.T) {
	raw := readExample(t, "simple-report.json")
	raw = bytes.Replace(raw, []byte(`"title": "月度销售趋势",`),
		[]byte(`"title": "月度销售趋势", "richText": "<p onclick=\"x\">安全<script>drop</script></p>",`), 1)
	definition, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := definition.Components[0].Options.RichText
	if strings.Contains(got, "onclick") || strings.Contains(got, "script") || strings.Contains(got, "drop") ||
		!strings.Contains(got, "安全") {
		t.Fatalf("Decode() richText = %q", got)
	}
}
