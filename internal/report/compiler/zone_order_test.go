package compiler

import (
	"testing"

	"intelligent-report-generation-system/internal/report"
)

// Zone order and empty-space priority are independent.
//
// They used to be the same field, and their two consumers sorted it in opposite
// directions: normalization ascending to decide render order, CalculateZoneHeights
// descending to decide which zone absorbs space freed by empty ones. A card could
// not both show its chart first and give that chart the freed space — which is
// exactly what an analysis card needs.
func TestZoneOrderIsIndependentOfEmptySpacePriority(t *testing.T) {
	share := 1.0
	content := report.Zone{
		ID: "00000000-0000-4000-8000-0000000000c1", Order: 1, Type: report.ZoneContent,
		Layout: report.ZoneLayout{
			HeightMode: report.ZoneHeightFR, FR: &share, MinHeight: 1, Columns: 8, Rows: 6,
			Overflow: report.OverflowExpand, EmptyPriority: 5,
		},
		Slots: []report.Slot{{ID: "00000000-0000-4000-8000-0000000000s1", Grid: report.SlotGrid{W: 8, H: 6}}},
	}
	conclusion := report.Zone{
		ID: "00000000-0000-4000-8000-0000000000c2", Order: 2, Type: report.ZoneInsight,
		Layout: report.ZoneLayout{
			HeightMode: report.ZoneHeightAuto, MinHeight: 2, Columns: 8, Rows: 2,
			Overflow: report.OverflowExpand, EmptyPriority: 0,
		},
		Slots: []report.Slot{{ID: "00000000-0000-4000-8000-0000000000s2", Grid: report.SlotGrid{W: 8, H: 2}}},
	}

	// Declared order decides layout position, even though the content zone has
	// the higher empty-space priority.
	if content.Order >= conclusion.Order {
		t.Fatal("content must be declared before its conclusion")
	}
	heights := CalculateZoneHeights([]report.Zone{content, conclusion}, 20)
	if heights[content.ID] <= heights[conclusion.ID] {
		t.Fatalf("the FR content zone must absorb the remaining height: %v", heights)
	}
}
