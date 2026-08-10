package search

import (
	"errors"
	"testing"
)

func TestRouteVectorSearchUsesExactBelowBoundedThreshold(t *testing.T) {
	for _, test := range []struct {
		name      string
		estimate  int
		want      VectorSearchRoute
		wantError bool
	}{
		{name: "empty", estimate: 0, want: VectorRouteExact},
		{name: "bounded", estimate: 999, want: VectorRouteExact},
		{name: "threshold", estimate: 1_000, want: VectorRouteANN},
		{name: "large", estimate: 50_000, want: VectorRouteANN},
		{name: "invalid", estimate: -1, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			route, err := RouteVectorSearch(test.estimate)
			if test.wantError {
				if !errors.Is(err, ErrInvalidVectorCandidateEstimate) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || route != test.want {
				t.Fatalf("route = %q, %v, want %q", route, err, test.want)
			}
		})
	}
}
