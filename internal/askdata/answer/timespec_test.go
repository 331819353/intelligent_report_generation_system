package answer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"intelligent-report-generation-system/internal/askdata/compiler"
)

type timeSpecFixture struct {
	Name     string                    `json:"name"`
	Input    compiler.ResolvedTimeSpec `json:"input"`
	Options  RenderOptions             `json:"options"`
	Expected TimeSpecView              `json:"expected"`
}

func TestRenderTimeSpecSharedFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testfixture", "timespec", "render-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []timeSpecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 20 {
		t.Fatalf("fixture count = %d, want at least 20", len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			actual := RenderTimeSpec(fixture.Input, fixture.Options)
			if !reflect.DeepEqual(actual, fixture.Expected) {
				t.Fatalf("view mismatch:\nactual:   %#v\nexpected: %#v", actual, fixture.Expected)
			}
		})
	}
}

func TestRenderTimeSpecFailsClosed(t *testing.T) {
	if actual := RenderTimeSpec(compiler.ResolvedTimeSpec{}, RenderOptions{}); actual != (TimeSpecView{}) {
		t.Fatalf("invalid spec rendered as %#v", actual)
	}
}
