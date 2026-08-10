package compiler

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/tiendc/go-deepcopy"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/template"
)

type LayoutError struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Collision struct {
	FirstID  askdata.ID `json:"firstId"`
	SecondID askdata.ID `json:"secondId"`
}

// ValidateLayout validates constraints which deliberately live outside the
// structural Report Definition validator: collisions and renderable minimums.
func ValidateLayout(definition report.ReportDefinition) []LayoutError {
	var issues []LayoutError
	for pageIndex, page := range definition.Pages {
		for sectionIndex, section := range page.Sections {
			path := fmt.Sprintf("pages[%d].sections[%d]", pageIndex, sectionIndex)
			for _, collision := range DetectCollisions(section.Blocks) {
				issues = append(issues, LayoutError{
					Code: "REPORT_LAYOUT_COLLISION", Path: path + ".blocks",
					Message: fmt.Sprintf("blocks %q and %q overlap", collision.FirstID, collision.SecondID),
				})
			}
			for blockIndex, block := range section.Blocks {
				blockPath := fmt.Sprintf("%s.blocks[%d]", path, blockIndex)
				if block.Layout.Desktop.X < 0 || block.Layout.Desktop.Y < 0 || block.Layout.Desktop.W < 1 || block.Layout.Desktop.H < 1 || block.Layout.Desktop.X+block.Layout.Desktop.W > definition.Canvas.Desktop.Columns {
					issues = append(issues, LayoutError{Code: "REPORT_LAYOUT_OUT_OF_BOUNDS", Path: blockPath + ".layout.desktop", Message: "block exceeds the desktop grid"})
				}
				mobilePrimaryCandidates := map[askdata.ID]struct{}{}
				for zoneIndex, zone := range block.Zones {
					zonePath := fmt.Sprintf("%s.zones[%d]", blockPath, zoneIndex)
					if zone.Layout.HeightMode != report.ZoneHeightHidden && zone.Layout.MinHeight <= 0 {
						issues = append(issues, LayoutError{Code: "REPORT_ZONE_MIN_HEIGHT_REQUIRED", Path: zonePath + ".layout.minHeight", Message: "visible zone requires a positive minHeight"})
					}
					for _, collision := range detectSlotCollisions(zone.Slots) {
						issues = append(issues, LayoutError{Code: "REPORT_LAYOUT_COLLISION", Path: zonePath + ".slots", Message: fmt.Sprintf("slots %q and %q overlap", collision.FirstID, collision.SecondID)})
					}
					for slotIndex, slot := range zone.Slots {
						if zone.Layout.HeightMode != report.ZoneHeightHidden && zone.Type != report.ZoneFilter {
							mobilePrimaryCandidates[slot.ID] = struct{}{}
						}
						if slot.Grid.X < 0 || slot.Grid.Y < 0 || slot.Grid.W < 1 || slot.Grid.H < 1 ||
							slot.Grid.X+slot.Grid.W > zone.Layout.Columns || slot.Grid.Y+slot.Grid.H > zone.Layout.Rows {
							issues = append(issues, LayoutError{Code: "REPORT_LAYOUT_OUT_OF_BOUNDS", Path: fmt.Sprintf("%s.slots[%d].grid", zonePath, slotIndex), Message: "slot exceeds the zone grid"})
						}
					}
				}
				if block.Layout.Mobile.SlotMode == report.MobileSlotPrimaryOnly {
					primary := block.Layout.Mobile.PrimarySlotID
					if primary == nil {
						issues = append(issues, LayoutError{Code: "REPORT_MOBILE_PRIMARY_SLOT_MISSING", Path: blockPath + ".layout.mobile.primarySlotId", Message: "PRIMARY_ONLY requires primarySlotId"})
					} else if _, exists := mobilePrimaryCandidates[*primary]; !exists {
						issues = append(issues, LayoutError{Code: "REPORT_MOBILE_PRIMARY_SLOT_MISSING", Path: blockPath + ".layout.mobile.primarySlotId", Message: fmt.Sprintf("primary slot %q does not exist in the block", *primary)})
					}
				}
			}
		}
	}
	return issues
}

type collisionItem struct {
	id         askdata.ID
	x, y, w, h int
	ordinal    int
}

type intervalNode struct {
	item           collisionItem
	left, right    *intervalNode
	height, maxEnd int
}

// DetectCollisions uses an x-axis sweep plus an AVL interval tree on y. Its
// complexity is O((n+k) log n), where k is the number of collisions returned.
func DetectCollisions(blocks []report.Block) []Collision {
	items := make([]collisionItem, 0, len(blocks))
	for index, block := range blocks {
		grid := block.Layout.Desktop
		if grid.W < 1 || grid.H < 1 {
			continue
		}
		items = append(items, collisionItem{id: block.ID, x: grid.X, y: grid.Y, w: grid.W, h: grid.H, ordinal: index})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].x != items[j].x {
			return items[i].x < items[j].x
		}
		if items[i].id != items[j].id {
			return items[i].id < items[j].id
		}
		return items[i].ordinal < items[j].ordinal
	})
	byEnd := append([]collisionItem(nil), items...)
	sort.Slice(byEnd, func(i, j int) bool {
		left, right := byEnd[i].x+byEnd[i].w, byEnd[j].x+byEnd[j].w
		if left != right {
			return left < right
		}
		return intervalItemLess(byEnd[i], byEnd[j])
	})
	var active *intervalNode
	expired := 0
	var result []Collision
	for _, current := range items {
		for expired < len(byEnd) && byEnd[expired].x+byEnd[expired].w <= current.x {
			active = intervalDelete(active, byEnd[expired])
			expired++
		}
		candidates := make([]collisionItem, 0)
		intervalOverlaps(active, current.y, current.y+current.h, &candidates)
		for _, candidate := range candidates {
			first, second := candidate.id, current.id
			if second < first {
				first, second = second, first
			}
			result = append(result, Collision{FirstID: first, SecondID: second})
		}
		active = intervalInsert(active, current)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].FirstID != result[j].FirstID {
			return result[i].FirstID < result[j].FirstID
		}
		return result[i].SecondID < result[j].SecondID
	})
	return result
}

func intervalItemLess(left, right collisionItem) bool {
	if left.y != right.y {
		return left.y < right.y
	}
	if left.id != right.id {
		return left.id < right.id
	}
	return left.ordinal < right.ordinal
}

func intervalHeight(node *intervalNode) int {
	if node == nil {
		return 0
	}
	return node.height
}

func intervalMaxEnd(node *intervalNode) int {
	if node == nil {
		return math.MinInt
	}
	return node.maxEnd
}

func updateIntervalNode(node *intervalNode) {
	node.height = 1 + max(intervalHeight(node.left), intervalHeight(node.right))
	node.maxEnd = max(node.item.y+node.item.h, max(intervalMaxEnd(node.left), intervalMaxEnd(node.right)))
}

func rotateIntervalRight(root *intervalNode) *intervalNode {
	next := root.left
	root.left = next.right
	next.right = root
	updateIntervalNode(root)
	updateIntervalNode(next)
	return next
}

func rotateIntervalLeft(root *intervalNode) *intervalNode {
	next := root.right
	root.right = next.left
	next.left = root
	updateIntervalNode(root)
	updateIntervalNode(next)
	return next
}

func balanceIntervalNode(node *intervalNode) *intervalNode {
	if node == nil {
		return nil
	}
	updateIntervalNode(node)
	balance := intervalHeight(node.left) - intervalHeight(node.right)
	if balance > 1 {
		if intervalHeight(node.left.left) < intervalHeight(node.left.right) {
			node.left = rotateIntervalLeft(node.left)
		}
		return rotateIntervalRight(node)
	}
	if balance < -1 {
		if intervalHeight(node.right.right) < intervalHeight(node.right.left) {
			node.right = rotateIntervalRight(node.right)
		}
		return rotateIntervalLeft(node)
	}
	return node
}

func intervalInsert(node *intervalNode, item collisionItem) *intervalNode {
	if node == nil {
		return &intervalNode{item: item, height: 1, maxEnd: item.y + item.h}
	}
	if intervalItemLess(item, node.item) {
		node.left = intervalInsert(node.left, item)
	} else {
		node.right = intervalInsert(node.right, item)
	}
	return balanceIntervalNode(node)
}

func intervalDelete(node *intervalNode, item collisionItem) *intervalNode {
	if node == nil {
		return nil
	}
	if intervalItemLess(item, node.item) {
		node.left = intervalDelete(node.left, item)
	} else if intervalItemLess(node.item, item) {
		node.right = intervalDelete(node.right, item)
	} else if node.left == nil {
		return node.right
	} else if node.right == nil {
		return node.left
	} else {
		successor := node.right
		for successor.left != nil {
			successor = successor.left
		}
		node.item = successor.item
		node.right = intervalDelete(node.right, successor.item)
	}
	return balanceIntervalNode(node)
}

func intervalOverlaps(node *intervalNode, minY, maxY int, result *[]collisionItem) {
	if node == nil || node.maxEnd <= minY {
		return
	}
	intervalOverlaps(node.left, minY, maxY, result)
	if node.item.y < maxY && node.item.y+node.item.h > minY {
		*result = append(*result, node.item)
	}
	if node.item.y < maxY {
		intervalOverlaps(node.right, minY, maxY, result)
	}
}

func detectSlotCollisions(slots []report.Slot) []Collision {
	var result []Collision
	for left := range slots {
		for right := left + 1; right < len(slots); right++ {
			a, b := slots[left], slots[right]
			if rectanglesIntersect(a.Grid.X, a.Grid.Y, a.Grid.W, a.Grid.H, b.Grid.X, b.Grid.Y, b.Grid.W, b.Grid.H) {
				first, second := a.ID, b.ID
				if second < first {
					first, second = second, first
				}
				result = append(result, Collision{FirstID: first, SecondID: second})
			}
		}
	}
	return result
}

func rectanglesIntersect(ax, ay, aw, ah, bx, by, bw, bh int) bool {
	return ax < bx+bw && bx < ax+aw && ay < by+bh && by < ay+ah
}

type PixelRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// DesktopPixelRect converts logical desktop grid coordinates at runtime. The
// result is deliberately not persisted in Report Definition JSON.
func DesktopPixelRect(canvas report.DesktopCanvas, grid report.DesktopBlockLayout, containerWidth float64) (PixelRect, error) {
	if canvas.Columns < 1 || containerWidth <= 0 || grid.X < 0 || grid.Y < 0 || grid.W < 1 || grid.H < 1 ||
		grid.X+grid.W > canvas.Columns {
		return PixelRect{}, errors.New("desktop pixel conversion input is invalid")
	}
	usableWidth := containerWidth - 2*float64(canvas.PaddingX) - float64(canvas.Columns-1)*float64(canvas.GapX)
	if usableWidth <= 0 {
		return PixelRect{}, errors.New("desktop container is too narrow for the configured grid")
	}
	columnWidth := usableWidth / float64(canvas.Columns)
	return PixelRect{
		X:      float64(canvas.PaddingX) + float64(grid.X)*(columnWidth+float64(canvas.GapX)),
		Y:      float64(canvas.PaddingY) + float64(grid.Y)*float64(canvas.BaseRowHeight+canvas.GapY),
		Width:  float64(grid.W)*columnWidth + float64(grid.W-1)*float64(canvas.GapX),
		Height: float64(grid.H*canvas.BaseRowHeight + (grid.H-1)*canvas.GapY),
	}, nil
}

type CompactMode string

const (
	CompactNone     CompactMode = "NONE"
	CompactVertical CompactMode = "VERTICAL"
)

// CompactBlocks applies the immutable layout-template compact mode to a copy.
func CompactBlocks(blocks []report.Block, mode CompactMode) ([]report.Block, error) {
	var result []report.Block
	if err := deepcopy.Copy(&result, &blocks); err != nil {
		return nil, fmt.Errorf("clone blocks for compaction: %w", err)
	}
	if mode == CompactNone {
		return result, nil
	}
	if mode != CompactVertical {
		return nil, fmt.Errorf("unsupported compactMode %q", mode)
	}
	order := make([]int, len(result))
	for index := range order {
		order[index] = index
	}
	sort.Slice(order, func(left, right int) bool {
		a, b := result[order[left]], result[order[right]]
		if a.Layout.Desktop.Y != b.Layout.Desktop.Y {
			return a.Layout.Desktop.Y < b.Layout.Desktop.Y
		}
		if a.Layout.Desktop.X != b.Layout.Desktop.X {
			return a.Layout.Desktop.X < b.Layout.Desktop.X
		}
		return a.ID < b.ID
	})
	placed := make([]int, 0, len(result))
	for _, index := range order {
		current := &result[index]
		top := 0
		for _, previousIndex := range placed {
			previous := result[previousIndex].Layout.Desktop
			if previous.X < current.Layout.Desktop.X+current.Layout.Desktop.W &&
				current.Layout.Desktop.X < previous.X+previous.W {
				top = max(top, previous.Y+previous.H)
			}
		}
		current.Layout.Desktop.Y = top
		placed = append(placed, index)
	}
	return result, nil
}

type SlotMergeError struct {
	Code    string
	Message string
}

func (err *SlotMergeError) Error() string { return err.Code + ": " + err.Message }

func ValidateSlotMerge(zone report.Zone, slotIDs []askdata.ID) error {
	return ValidateSlotMergeWithMinSize(zone, slotIDs, template.GridSize{W: 1, H: 1})
}

func ValidateSlotMergeWithMinSize(zone report.Zone, slotIDs []askdata.ID, minimum template.GridSize) error {
	if len(slotIDs) < 2 {
		return &SlotMergeError{Code: "REPORT_SLOT_MERGE_NOT_RECTANGULAR", Message: "at least two slots are required"}
	}
	wanted := make(map[askdata.ID]struct{}, len(slotIDs))
	for _, id := range slotIDs {
		if _, duplicate := wanted[id]; duplicate {
			return &SlotMergeError{Code: "REPORT_SLOT_MERGE_NOT_RECTANGULAR", Message: fmt.Sprintf("slot %q is duplicated", id)}
		}
		wanted[id] = struct{}{}
	}
	selected := make([]report.Slot, 0, len(slotIDs))
	for _, slot := range zone.Slots {
		if _, exists := wanted[slot.ID]; exists {
			selected = append(selected, slot)
			delete(wanted, slot.ID)
		}
	}
	if len(wanted) != 0 {
		return &SlotMergeError{Code: "REPORT_SLOT_MERGE_NOT_RECTANGULAR", Message: "all slots must exist in the same zone"}
	}
	minX, minY := math.MaxInt, math.MaxInt
	maxX, maxY, area := 0, 0, 0
	occupied := 0
	for _, slot := range selected {
		minX = min(minX, slot.Grid.X)
		minY = min(minY, slot.Grid.Y)
		maxX = max(maxX, slot.Grid.X+slot.Grid.W)
		maxY = max(maxY, slot.Grid.Y+slot.Grid.H)
		area += slot.Grid.W * slot.Grid.H
		if slot.ComponentID != "" {
			occupied++
		}
	}
	if len(detectSlotCollisions(selected)) != 0 || area != (maxX-minX)*(maxY-minY) {
		return &SlotMergeError{Code: "REPORT_SLOT_MERGE_NOT_RECTANGULAR", Message: "selected slots must form one continuous rectangle without holes"}
	}
	if occupied > 1 {
		return &SlotMergeError{Code: "REPORT_SLOT_MERGE_MULTIPLE_COMPONENTS", Message: "selected slots contain more than one component"}
	}
	if maxX-minX < minimum.W || maxY-minY < minimum.H {
		return &SlotMergeError{Code: "REPORT_SLOT_MERGE_BELOW_MIN_SIZE", Message: fmt.Sprintf("merged slot is below minimum %dx%d", minimum.W, minimum.H)}
	}
	return nil
}

// MergeSlots derives the merged geometry and component placement from the
// selected slots. Callers provide only the new stable ID; persisted geometry
// never trusts a preview/client calculation.
func MergeSlots(zone report.Zone, slotIDs []askdata.ID, newID askdata.ID, minimum template.GridSize) (report.Slot, error) {
	if err := ValidateSlotMergeWithMinSize(zone, slotIDs, minimum); err != nil {
		return report.Slot{}, err
	}
	if err := newID.Validate(); err != nil {
		return report.Slot{}, &SlotMergeError{Code: "REPORT_SLOT_MERGE_NOT_RECTANGULAR", Message: "new slot ID is invalid"}
	}
	wanted := make(map[askdata.ID]struct{}, len(slotIDs))
	for _, id := range slotIDs {
		if id == newID {
			return report.Slot{}, &SlotMergeError{Code: "REPORT_SLOT_MERGE_NOT_RECTANGULAR", Message: "new slot ID must differ from source slot IDs"}
		}
		wanted[id] = struct{}{}
	}
	minX, minY := math.MaxInt, math.MaxInt
	maxX, maxY := 0, 0
	componentID := askdata.ID("")
	for _, slot := range zone.Slots {
		if _, selected := wanted[slot.ID]; !selected {
			continue
		}
		minX = min(minX, slot.Grid.X)
		minY = min(minY, slot.Grid.Y)
		maxX = max(maxX, slot.Grid.X+slot.Grid.W)
		maxY = max(maxY, slot.Grid.Y+slot.Grid.H)
		if slot.ComponentID != "" {
			componentID = slot.ComponentID
		}
	}
	mergedFrom := append([]askdata.ID(nil), slotIDs...)
	sort.Slice(mergedFrom, func(left, right int) bool { return mergedFrom[left] < mergedFrom[right] })
	return report.Slot{
		ID: newID, Grid: report.SlotGrid{X: minX, Y: minY, W: maxX - minX, H: maxY - minY},
		ComponentID: componentID, MergedFrom: mergedFrom,
	}, nil
}

// SlotRenderSize maps a slot's internal zone grid back to the enclosing
// desktop grid so Component Manifest minSize remains expressed in one unit.
func SlotRenderSize(block report.Block, zone report.Zone, grid report.SlotGrid) template.GridSize {
	if block.Layout.Desktop.W < 1 || block.Layout.Desktop.H < 1 || zone.Layout.Columns < 1 || zone.Layout.Rows < 1 || grid.W < 1 || grid.H < 1 {
		return template.GridSize{}
	}
	return template.GridSize{
		W: (block.Layout.Desktop.W*grid.W + zone.Layout.Columns - 1) / zone.Layout.Columns,
		H: (block.Layout.Desktop.H*grid.H + zone.Layout.Rows - 1) / zone.Layout.Rows,
	}
}

type MobilePage struct {
	ID     askdata.ID    `json:"id"`
	Blocks []MobileBlock `json:"blocks"`
}

type MobileBlock struct {
	ID                  askdata.ID              `json:"id"`
	Order               int                     `json:"order"`
	HeightMode          report.MobileHeightMode `json:"heightMode"`
	SlotMode            report.MobileSlotMode   `json:"slotMode"`
	Slots               []report.Slot           `json:"slots"`
	FilterDrawerSlots   []report.Slot           `json:"filterDrawerSlots"`
	QueriedComponentIDs []askdata.ID            `json:"queriedComponentIds"`
	PrimarySlotID       *askdata.ID             `json:"primarySlotId,omitempty"`
	FixedHeight         *int                    `json:"fixedHeight,omitempty"`
	AspectRatio         *float64                `json:"aspectRatio,omitempty"`
	FullWidth           bool                    `json:"fullWidth"`
	ComponentPolicies   []MobileComponentPolicy `json:"componentPolicies,omitempty"`
}

type MobileComponentPolicy struct {
	ComponentID      askdata.ID                `json:"componentId"`
	Supported        bool                      `json:"supported"`
	LegendMode       template.LegendMode       `json:"legendMode"`
	LabelDegradation template.LabelDegradation `json:"labelDegradation"`
}

func ToMobileLayout(page report.Page) MobilePage {
	result, _ := ToMobileLayoutWithComponents(page, nil, nil)
	return result
}

// ToMobileLayoutWithComponents attaches the exact Manifest mobile policy that
// the renderer must use for legend and label degradation.
func ToMobileLayoutWithComponents(page report.Page, components []report.Component, registry *template.Registry) (MobilePage, error) {
	componentsByID := make(map[askdata.ID]report.Component, len(components))
	for _, component := range components {
		componentsByID[component.ID] = component
	}
	if len(components) != 0 && registry == nil {
		var err error
		registry, err = defaultRegistry()
		if err != nil {
			return MobilePage{}, err
		}
	}
	result := MobilePage{ID: page.ID, Blocks: []MobileBlock{}}
	for _, section := range page.Sections {
		for _, block := range section.Blocks {
			if !block.Layout.Mobile.Visible {
				continue
			}
			slots := make([]report.Slot, 0)
			filterSlots := make([]report.Slot, 0)
			for _, zone := range block.Zones {
				if zone.Layout.HeightMode != report.ZoneHeightHidden {
					if zone.Type == report.ZoneFilter {
						filterSlots = append(filterSlots, zone.Slots...)
					} else {
						slots = append(slots, zone.Slots...)
					}
				}
			}
			sortSlots := func(values []report.Slot) {
				sort.Slice(values, func(i, j int) bool {
					if values[i].Grid.Y != values[j].Grid.Y {
						return values[i].Grid.Y < values[j].Grid.Y
					}
					if values[i].Grid.X != values[j].Grid.X {
						return values[i].Grid.X < values[j].Grid.X
					}
					return values[i].ID < values[j].ID
				})
			}
			sortSlots(slots)
			sortSlots(filterSlots)
			if block.Layout.Mobile.SlotMode == report.MobileSlotPrimaryOnly {
				selected := slots[:0]
				if block.Layout.Mobile.PrimarySlotID != nil {
					for _, slot := range slots {
						if slot.ID == *block.Layout.Mobile.PrimarySlotID {
							selected = append(selected, slot)
							break
						}
					}
				}
				slots = selected
			}
			componentIDs := make([]askdata.ID, 0, len(slots))
			policies := make([]MobileComponentPolicy, 0, len(slots))
			for _, slot := range slots {
				if slot.ComponentID != "" {
					componentIDs = append(componentIDs, slot.ComponentID)
					if component, exists := componentsByID[slot.ComponentID]; exists {
						manifest, exists := registry.Get(component.TemplateRef.Type, component.TemplateRef.Version)
						if !exists {
							return MobilePage{}, fmt.Errorf("component manifest %s@%s is not registered", component.TemplateRef.Type, component.TemplateRef.Version)
						}
						policies = append(policies, MobileComponentPolicy{
							ComponentID: component.ID, Supported: manifest.MobilePolicy.Supported,
							LegendMode:       manifest.MobilePolicy.DefaultLegendMode,
							LabelDegradation: manifest.MobilePolicy.LabelDegradation,
						})
					}
				}
			}
			result.Blocks = append(result.Blocks, MobileBlock{
				ID: block.ID, Order: block.Layout.Mobile.Order, HeightMode: block.Layout.Mobile.HeightMode,
				SlotMode: block.Layout.Mobile.SlotMode, Slots: slots, FilterDrawerSlots: filterSlots,
				QueriedComponentIDs: componentIDs, PrimarySlotID: block.Layout.Mobile.PrimarySlotID,
				FixedHeight: block.Layout.Mobile.FixedHeight, AspectRatio: block.Layout.Mobile.AspectRatio,
				FullWidth: true, ComponentPolicies: policies,
			})
		}
	}
	sort.Slice(result.Blocks, func(i, j int) bool {
		if result.Blocks[i].Order != result.Blocks[j].Order {
			return result.Blocks[i].Order < result.Blocks[j].Order
		}
		return result.Blocks[i].ID < result.Blocks[j].ID
	})
	return result, nil
}

func MobileBlockHeight(layout report.MobileBlockLayout, containerWidth, autoHeight int) (int, error) {
	switch layout.HeightMode {
	case report.MobileHeightAuto:
		if autoHeight < 0 {
			return 0, errors.New("auto height cannot be negative")
		}
		return autoHeight, nil
	case report.MobileHeightFixed:
		if layout.FixedHeight == nil || *layout.FixedHeight < 40 {
			return 0, errors.New("FIXED mobile height is invalid")
		}
		return *layout.FixedHeight, nil
	case report.MobileHeightAspectRatio:
		if containerWidth <= 0 || layout.AspectRatio == nil || *layout.AspectRatio <= 0 {
			return 0, errors.New("ASPECT_RATIO mobile height is invalid")
		}
		return int(math.Round(float64(containerWidth) / *layout.AspectRatio)), nil
	default:
		return 0, errors.New("mobile height mode is invalid")
	}
}

// CalculateZoneHeights deterministically assigns a logical height. AUTO uses
// minHeight; FIXED uses fixedHeight; FR shares the remaining space by weight
// while respecting minimums; HIDDEN is zero.
func CalculateZoneHeights(zones []report.Zone, totalHeight int) map[askdata.ID]int {
	priority := make([]askdata.ID, 0, len(zones))
	ordered := append([]report.Zone(nil), zones...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Layout.EmptyPriority != ordered[right].Layout.EmptyPriority {
			return ordered[left].Layout.EmptyPriority > ordered[right].Layout.EmptyPriority
		}
		return ordered[left].ID < ordered[right].ID
	})
	for _, zone := range ordered {
		priority = append(priority, zone.ID)
	}
	return CalculateZoneHeightsWithPriority(zones, totalHeight, priority)
}

// CalculateZoneHeightsWithPriority applies the layout template's explicit
// empty-zone redistribution order after resolving FIXED/AUTO/FR/HIDDEN modes.
func CalculateZoneHeightsWithPriority(zones []report.Zone, totalHeight int, priority []askdata.ID) map[askdata.ID]int {
	result := make(map[askdata.ID]int, len(zones))
	remaining := totalHeight
	var weighted []report.Zone
	totalWeight := 0.0
	for _, zone := range zones {
		switch zone.Layout.HeightMode {
		case report.ZoneHeightHidden:
			result[zone.ID] = 0
		case report.ZoneHeightFixed:
			height := zone.Layout.MinHeight
			if zone.Layout.FixedHeight != nil {
				height = *zone.Layout.FixedHeight
			}
			result[zone.ID] = height
			remaining -= height
		case report.ZoneHeightAuto:
			result[zone.ID] = zone.Layout.MinHeight
			remaining -= zone.Layout.MinHeight
		case report.ZoneHeightFR:
			weighted = append(weighted, zone)
			result[zone.ID] = zone.Layout.MinHeight
			remaining -= zone.Layout.MinHeight
			if zone.Layout.FR != nil && *zone.Layout.FR > 0 {
				totalWeight += *zone.Layout.FR
			}
		}
	}
	for remaining > 0 && len(weighted) > 0 && totalWeight > 0 {
		available := remaining
		progress := 0
		next := make([]report.Zone, 0, len(weighted))
		nextWeight := 0.0
		for _, zone := range weighted {
			share := max(1, int(math.Floor(float64(available)*(*zone.Layout.FR)/totalWeight)))
			share = min(share, remaining)
			if zone.Layout.MaxHeight != nil {
				share = min(share, *zone.Layout.MaxHeight-result[zone.ID])
			}
			if share > 0 {
				result[zone.ID] += share
				remaining -= share
				progress += share
			}
			if zone.Layout.MaxHeight == nil || result[zone.ID] < *zone.Layout.MaxHeight {
				next = append(next, zone)
				nextWeight += *zone.Layout.FR
			}
			if remaining == 0 {
				break
			}
		}
		if progress == 0 {
			break
		}
		weighted, totalWeight = next, nextWeight
	}

	byID := make(map[askdata.ID]report.Zone, len(zones))
	freed := 0
	for _, zone := range zones {
		byID[zone.ID] = zone
		if zone.Layout.HeightMode != report.ZoneHeightHidden && len(zone.Slots) == 0 {
			freed += result[zone.ID]
			result[zone.ID] = 0
		}
	}
	for _, id := range priority {
		if freed == 0 {
			break
		}
		zone, exists := byID[id]
		if !exists || len(zone.Slots) == 0 ||
			(zone.Layout.HeightMode != report.ZoneHeightAuto && zone.Layout.HeightMode != report.ZoneHeightFR) {
			continue
		}
		capacity := freed
		if zone.Layout.MaxHeight != nil {
			capacity = min(capacity, *zone.Layout.MaxHeight-result[id])
		}
		if capacity > 0 {
			result[id] += capacity
			freed -= capacity
		}
	}
	return result
}
