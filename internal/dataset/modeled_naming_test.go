package dataset

import "testing"

func TestModeledDWDDisplayNameUsesChineseBusinessTitle(t *testing.T) {
	tests := []struct {
		name     string
		proposed string
		fact     string
		want     string
	}{
		{
			name:     "keeps chinese title",
			proposed: "订单明细宽表",
			fact:     "订单明细表",
			want:     "订单明细宽表",
		},
		{
			name:     "removes attached layer suffix",
			proposed: "配送事件事实表DWD",
			fact:     "配送事件表",
			want:     "配送事件事实表",
		},
		{
			name:     "falls back from english title",
			proposed: "SNAPSHOT",
			fact:     "商户日度运营聚合表",
			want:     "商户日度运营聚合表",
		},
		{
			name:     "uses safe chinese fallback",
			proposed: "dwd_snapshot",
			fact:     "fact_orders",
			want:     "业务明细事实表",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ModeledDWDDisplayName(test.proposed, test.fact); got != test.want {
				t.Fatalf("ModeledDWDDisplayName(%q, %q) = %q, want %q",
					test.proposed, test.fact, got, test.want)
			}
		})
	}
}
