package metadataai

import "testing"

func TestConfiguredFallbackModelFollowsRoundRobinRing(t *testing.T) {
	models := "MiniMax-M2,deepseek-v4-flash,glm-5.2"
	for _, test := range []struct {
		current string
		want    string
	}{
		{current: "", want: "deepseek-v4-flash"},
		{current: "MiniMax-M2", want: "deepseek-v4-flash"},
		{current: "deepseek-v4-flash", want: "glm-5.2"},
		{current: "glm-5.2", want: "MiniMax-M2"},
	} {
		if got := configuredFallbackModel(models, test.current); got != test.want {
			t.Fatalf("current %q: got %q, want %q", test.current, got, test.want)
		}
	}
}
