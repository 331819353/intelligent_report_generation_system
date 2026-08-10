package compiler

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/template"
)

func TestValidateLayoutStableErrorsAndExample(t *testing.T) {
	definition := compilerDefinition(t)
	block := &definition.Pages[0].Sections[0].Blocks[0]

	t.Run("out of bounds and collision", func(t *testing.T) {
		copy := definition
		copy.Pages = append([]report.Page(nil), definition.Pages...)
		copy.Pages[0].Sections = append([]report.Section(nil), definition.Pages[0].Sections...)
		copy.Pages[0].Sections[0].Blocks = append([]report.Block(nil), definition.Pages[0].Sections[0].Blocks...)
		copy.Pages[0].Sections[0].Blocks[0].Layout.Desktop.X = 23
		second := *block
		second.ID = "block_second"
		second.Layout.Desktop = copy.Pages[0].Sections[0].Blocks[0].Layout.Desktop
		copy.Pages[0].Sections[0].Blocks = append(copy.Pages[0].Sections[0].Blocks, second)
		assertLayoutCode(t, ValidateLayout(copy), "REPORT_LAYOUT_OUT_OF_BOUNDS")
		assertLayoutCode(t, ValidateLayout(copy), "REPORT_LAYOUT_COLLISION")
	})

	t.Run("FR min height", func(t *testing.T) {
		copy := definition
		copy.Pages[0].Sections[0].Blocks[0].Zones[0].Layout.HeightMode = report.ZoneHeightFR
		copy.Pages[0].Sections[0].Blocks[0].Zones[0].Layout.MinHeight = 0
		weight := 1.0
		copy.Pages[0].Sections[0].Blocks[0].Zones[0].Layout.FR = &weight
		assertLayoutCode(t, ValidateLayout(copy), "REPORT_ZONE_MIN_HEIGHT_REQUIRED")
	})

	t.Run("PRIMARY_ONLY target", func(t *testing.T) {
		copy := definition
		copy.Pages[0].Sections[0].Blocks[0].Layout.Mobile.SlotMode = report.MobileSlotPrimaryOnly
		missing := askdata.ID("slot_missing")
		copy.Pages[0].Sections[0].Blocks[0].Layout.Mobile.PrimarySlotID = &missing
		assertLayoutCode(t, ValidateLayout(copy), "REPORT_MOBILE_PRIMARY_SLOT_MISSING")
	})

	t.Run("multi-page contract compiles", func(t *testing.T) {
		_, filename, _, _ := runtime.Caller(0)
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api", "examples", "report-definition", "multi-page-report.json"))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := report.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Normalize(decoded); err != nil {
			t.Fatalf("multi-page example does not compile: %v", err)
		}
	})
}

func TestDetectCollisionsHandles300BlocksUnderTenMilliseconds(t *testing.T) {
	blocks := make([]report.Block, 300)
	for index := range blocks {
		blocks[index] = report.Block{
			ID:     askdata.ID(fmt.Sprintf("block_%03d", index)),
			Layout: report.BlockLayout{Desktop: report.DesktopBlockLayout{X: index % 24, Y: index * 2, W: 1, H: 1}},
		}
	}
	started := time.Now()
	collisions := DetectCollisions(blocks)
	elapsed := time.Since(started)
	if len(collisions) != 0 {
		t.Fatalf("unexpected collisions: %#v", collisions)
	}
	if elapsed >= 10*time.Millisecond {
		t.Fatalf("300-block collision detection took %s", elapsed)
	}
}

func TestDetectCollisionsMatchesBruteForce(t *testing.T) {
	random := rand.New(rand.NewSource(300))
	for iteration := 0; iteration < 100; iteration++ {
		blocks := make([]report.Block, 80)
		for index := range blocks {
			blocks[index] = report.Block{
				ID: askdata.ID(fmt.Sprintf("block_%03d", index)),
				Layout: report.BlockLayout{Desktop: report.DesktopBlockLayout{
					X: random.Intn(24), Y: random.Intn(80), W: 1 + random.Intn(6), H: 1 + random.Intn(8),
				}},
			}
		}
		want := make([]Collision, 0)
		for left := range blocks {
			for right := left + 1; right < len(blocks); right++ {
				a, b := blocks[left], blocks[right]
				if rectanglesIntersect(a.Layout.Desktop.X, a.Layout.Desktop.Y, a.Layout.Desktop.W, a.Layout.Desktop.H,
					b.Layout.Desktop.X, b.Layout.Desktop.Y, b.Layout.Desktop.W, b.Layout.Desktop.H) {
					first, second := a.ID, b.ID
					if second < first {
						first, second = second, first
					}
					want = append(want, Collision{FirstID: first, SecondID: second})
				}
			}
		}
		slices.SortFunc(want, func(left, right Collision) int {
			if left.FirstID < right.FirstID {
				return -1
			}
			if left.FirstID > right.FirstID {
				return 1
			}
			if left.SecondID < right.SecondID {
				return -1
			}
			if left.SecondID > right.SecondID {
				return 1
			}
			return 0
		})
		got := DetectCollisions(blocks)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("iteration %d mismatch: got=%v want=%v", iteration, got, want)
		}
	}
}

func TestDesktopPixelRectAndCompaction(t *testing.T) {
	canvas := report.DesktopCanvas{Columns: 24, BaseRowHeight: 32, GapX: 8, GapY: 8, PaddingX: 16, PaddingY: 12}
	got, err := DesktopPixelRect(canvas, report.DesktopBlockLayout{X: 2, Y: 3, W: 4, H: 5}, 1200)
	if err != nil {
		t.Fatal(err)
	}
	want := PixelRect{X: 114, Y: 132, Width: 188, Height: 192}
	if got != want {
		t.Fatalf("pixel rect = %#v, want %#v", got, want)
	}

	blocks := []report.Block{
		{ID: "block_a", Layout: report.BlockLayout{Desktop: report.DesktopBlockLayout{X: 0, Y: 4, W: 6, H: 2}}},
		{ID: "block_b", Layout: report.BlockLayout{Desktop: report.DesktopBlockLayout{X: 0, Y: 9, W: 6, H: 2}}},
		{ID: "block_c", Layout: report.BlockLayout{Desktop: report.DesktopBlockLayout{X: 8, Y: 7, W: 6, H: 2}}},
	}
	vertical, err := CompactBlocks(blocks, CompactVertical)
	if err != nil {
		t.Fatal(err)
	}
	if vertical[0].Layout.Desktop.Y != 0 || vertical[1].Layout.Desktop.Y != 2 || vertical[2].Layout.Desktop.Y != 0 {
		t.Fatalf("vertical compaction = %#v", vertical)
	}
	if blocks[0].Layout.Desktop.Y != 4 {
		t.Fatal("compaction mutated caller blocks")
	}
	none, err := CompactBlocks(blocks, CompactNone)
	if err != nil || none[1].Layout.Desktop.Y != 9 {
		t.Fatalf("NONE compaction = %#v, %v", none, err)
	}
}

func TestCalculateZoneHeightsAndTemplatePriority(t *testing.T) {
	fixedHeight, maximum, weight := 80, 220, 1.0
	zones := []report.Zone{
		{ID: "zone_fixed", Layout: report.ZoneLayout{HeightMode: report.ZoneHeightFixed, MinHeight: 40, FixedHeight: &fixedHeight}, Slots: []report.Slot{{ID: "slot_fixed"}}},
		{ID: "zone_auto", Layout: report.ZoneLayout{HeightMode: report.ZoneHeightAuto, MinHeight: 40}, Slots: []report.Slot{{ID: "slot_auto"}}},
		{ID: "zone_fr", Layout: report.ZoneLayout{HeightMode: report.ZoneHeightFR, MinHeight: 50, MaxHeight: &maximum, FR: &weight}, Slots: []report.Slot{{ID: "slot_fr"}}},
		{ID: "zone_hidden", Layout: report.ZoneLayout{HeightMode: report.ZoneHeightHidden}, Slots: []report.Slot{{ID: "slot_hidden"}}},
	}
	heights := CalculateZoneHeights(zones, 300)
	if heights["zone_fixed"] != 80 || heights["zone_auto"] != 40 || heights["zone_fr"] != 180 || heights["zone_hidden"] != 0 {
		t.Fatalf("four height modes = %#v", heights)
	}

	empty := []report.Zone{
		{ID: "zone_empty", Layout: report.ZoneLayout{HeightMode: report.ZoneHeightAuto, MinHeight: 60}},
		{ID: "zone_content", Layout: report.ZoneLayout{HeightMode: report.ZoneHeightAuto, MinHeight: 40, MaxHeight: intPointer(100)}, Slots: []report.Slot{{ID: "slot_content"}}},
		{ID: "zone_insight", Layout: report.ZoneLayout{HeightMode: report.ZoneHeightAuto, MinHeight: 40, MaxHeight: intPointer(100)}, Slots: []report.Slot{{ID: "slot_insight"}}},
	}
	contentFirst := CalculateZoneHeightsWithPriority(empty, 140, []askdata.ID{"zone_content", "zone_insight"})
	insightFirst := CalculateZoneHeightsWithPriority(empty, 140, []askdata.ID{"zone_insight", "zone_content"})
	if contentFirst["zone_content"] != 100 || contentFirst["zone_insight"] != 40 ||
		insightFirst["zone_content"] != 40 || insightFirst["zone_insight"] != 100 {
		t.Fatalf("template priority was ignored: content=%#v insight=%#v", contentFirst, insightFirst)
	}
}

func TestEmptySlotsAreValidMergeTargets(t *testing.T) {
	definition := compilerDefinition(t)
	zone := &definition.Pages[0].Sections[0].Blocks[0].Zones[0]
	zone.Layout.Columns, zone.Layout.Rows = 2, 1
	zone.Slots[0].Grid = report.SlotGrid{X: 0, Y: 0, W: 1, H: 1}
	zone.Slots = append(zone.Slots, report.Slot{ID: "slot_empty", Grid: report.SlotGrid{X: 1, Y: 0, W: 1, H: 1}})
	if _, _, err := Normalize(definition); err != nil {
		t.Fatalf("empty design slot must be valid: %v", err)
	}
	merged, err := MergeSlots(*zone, []askdata.ID{zone.Slots[0].ID, zone.Slots[1].ID}, "slot_merged", template.GridSize{W: 2, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Grid != (report.SlotGrid{X: 0, Y: 0, W: 2, H: 1}) || merged.ComponentID != zone.Slots[0].ComponentID || len(merged.MergedFrom) != 2 {
		t.Fatalf("derived merged slot = %#v", merged)
	}
}

func TestMobileLayoutAllSlotModesAndHeightModes(t *testing.T) {
	primary := askdata.ID("slot_primary")
	base := report.Block{
		ID: "block_mobile",
		Layout: report.BlockLayout{Mobile: report.MobileBlockLayout{
			Order: 2, Visible: true, HeightMode: report.MobileHeightAuto, SlotMode: report.MobileSlotStack,
		}},
		Zones: []report.Zone{
			{ID: "zone_filter", Type: report.ZoneFilter, Layout: report.ZoneLayout{HeightMode: report.ZoneHeightAuto}, Slots: []report.Slot{{ID: "slot_filter", ComponentID: "component_filter"}}},
			{ID: "zone_content", Type: report.ZoneContent, Layout: report.ZoneLayout{HeightMode: report.ZoneHeightAuto}, Slots: []report.Slot{
				{ID: "slot_secondary", Grid: report.SlotGrid{X: 0}, ComponentID: "component_secondary"},
				{ID: primary, Grid: report.SlotGrid{X: 1}, ComponentID: "component_primary"},
			}},
		},
	}
	for _, mode := range []report.MobileSlotMode{report.MobileSlotStack, report.MobileSlotCarousel, report.MobileSlotCollapse, report.MobileSlotPrimaryOnly} {
		t.Run(string(mode), func(t *testing.T) {
			block := base
			block.Layout.Mobile.SlotMode = mode
			if mode == report.MobileSlotPrimaryOnly {
				block.Layout.Mobile.PrimarySlotID = &primary
			}
			page := report.Page{ID: "page_mobile", Sections: []report.Section{{ID: "section_mobile", Blocks: []report.Block{
				{ID: "block_hidden", Layout: report.BlockLayout{Mobile: report.MobileBlockLayout{Order: 1, Visible: false}}}, block,
			}}}}
			mobile := ToMobileLayout(page)
			if len(mobile.Blocks) != 1 || !mobile.Blocks[0].FullWidth || len(mobile.Blocks[0].FilterDrawerSlots) != 1 {
				t.Fatalf("mobile block conversion = %#v", mobile)
			}
			wantSlots := 2
			if mode == report.MobileSlotPrimaryOnly {
				wantSlots = 1
				if mobile.Blocks[0].Slots[0].ID != primary || fmt.Sprint(mobile.Blocks[0].QueriedComponentIDs) != "[component_primary]" {
					t.Fatalf("PRIMARY_ONLY queried hidden slot: %#v", mobile.Blocks[0])
				}
			}
			if len(mobile.Blocks[0].Slots) != wantSlots {
				t.Fatalf("slots = %d, want %d", len(mobile.Blocks[0].Slots), wantSlots)
			}
		})
	}

	fixed, ratio := 180, 2.0
	for _, test := range []struct {
		layout report.MobileBlockLayout
		want   int
	}{
		{layout: report.MobileBlockLayout{HeightMode: report.MobileHeightAuto}, want: 123},
		{layout: report.MobileBlockLayout{HeightMode: report.MobileHeightFixed, FixedHeight: &fixed}, want: 180},
		{layout: report.MobileBlockLayout{HeightMode: report.MobileHeightAspectRatio, AspectRatio: &ratio}, want: 160},
	} {
		got, err := MobileBlockHeight(test.layout, 320, 123)
		if err != nil || got != test.want {
			t.Fatalf("mobile height = %d, %v; want %d", got, err, test.want)
		}
	}

	definition := compilerDefinition(t)
	registry, err := template.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	withPolicies, err := ToMobileLayoutWithComponents(definition.Pages[0], definition.Components, registry)
	if err != nil {
		t.Fatal(err)
	}
	policy := withPolicies.Blocks[0].ComponentPolicies[0]
	if !policy.Supported || policy.LegendMode != template.LegendHidden || policy.LabelDegradation != template.LabelHideWhenDense {
		t.Fatalf("manifest mobile policy was not attached: %#v", policy)
	}
}

func TestGoLayoutContractFixture(t *testing.T) {
	fixture := loadLayoutContract(t)
	for _, test := range fixture.CollisionCases {
		t.Run("collision/"+test.Name, func(t *testing.T) {
			blocks := make([]report.Block, len(test.Blocks))
			for index, block := range test.Blocks {
				blocks[index] = report.Block{ID: block.ID, Layout: report.BlockLayout{Desktop: block.DesktopBlockLayout}}
			}
			got := DetectCollisions(blocks)
			pairs := make([][2]askdata.ID, len(got))
			for index, collision := range got {
				pairs[index] = [2]askdata.ID{collision.FirstID, collision.SecondID}
			}
			if fmt.Sprint(pairs) != fmt.Sprint(test.Expected) {
				t.Fatalf("collisions = %v, want %v", pairs, test.Expected)
			}
		})
	}
	for _, test := range fixture.MergeCases {
		t.Run("merge/"+test.Name, func(t *testing.T) {
			zone := report.Zone{ID: "zone_contract", Slots: make([]report.Slot, len(test.Slots))}
			for index, slot := range test.Slots {
				zone.Slots[index] = report.Slot{ID: slot.ID, Grid: slot.SlotGrid, ComponentID: slot.ComponentID}
			}
			err := ValidateSlotMergeWithMinSize(zone, test.SlotIDs, test.Minimum)
			code := ""
			var mergeErr *SlotMergeError
			if errors.As(err, &mergeErr) {
				code = mergeErr.Code
			} else if err != nil {
				t.Fatal(err)
			}
			if code != test.ExpectedCode {
				t.Fatalf("merge code = %q, want %q", code, test.ExpectedCode)
			}
		})
	}
}

type layoutContract struct {
	CollisionCases []struct {
		Name   string `json:"name"`
		Blocks []struct {
			ID askdata.ID `json:"id"`
			report.DesktopBlockLayout
		} `json:"blocks"`
		Expected [][2]askdata.ID `json:"expected"`
	} `json:"collisionCases"`
	MergeCases []struct {
		Name  string `json:"name"`
		Slots []struct {
			ID          askdata.ID `json:"id"`
			ComponentID askdata.ID `json:"componentId"`
			report.SlotGrid
		} `json:"slots"`
		SlotIDs      []askdata.ID      `json:"slotIds"`
		Minimum      template.GridSize `json:"minimum"`
		ExpectedCode string            `json:"expectedCode"`
	} `json:"mergeCases"`
}

func loadLayoutContract(t *testing.T) layoutContract {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api", "examples", "report-layout-contract-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture layoutContract
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func assertLayoutCode(t *testing.T, issues []LayoutError, code string) {
	t.Helper()
	for _, issue := range issues {
		if issue.Code == code {
			return
		}
	}
	t.Fatalf("layout issues %#v do not contain %s", issues, code)
}

func intPointer(value int) *int { return &value }
