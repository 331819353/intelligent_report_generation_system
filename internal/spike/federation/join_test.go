package federation

import "testing"

func TestHashJoinAndFanoutLimit(t *testing.T) {
	left := []Row{{"id": 1}, {"id": 2}}
	right := []Row{{"owner": 1, "value": "a"}, {"owner": 1, "value": "b"}}
	got, err := HashJoin(left, right, "id", "owner", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("got=%d err=%v", len(got), err)
	}
	if _, err := HashJoin(left, right, "id", "owner", 1); err != ErrOutputLimit {
		t.Fatalf("expected output limit, got %v", err)
	}
}

func BenchmarkHashJoin10K(b *testing.B) {
	left := make([]Row, 10000)
	right := make([]Row, 10000)
	for i := range left {
		left[i] = Row{"id": i}
		right[i] = Row{"owner": i}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = HashJoin(left, right, "id", "owner", 20000)
	}
}
