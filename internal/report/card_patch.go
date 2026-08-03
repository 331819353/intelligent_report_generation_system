package report

import (
	"fmt"
	"reflect"
	"sort"

	"intelligent-report-generation-system/internal/reportjson"
)

type cardDelta struct {
	added, removed, changed             map[string]bool
	report, layout, filters, extensions bool
}

func calculateCardDelta(before, after reportjson.Document) cardDelta {
	delta := cardDelta{added: map[string]bool{}, removed: map[string]bool{}, changed: map[string]bool{}}
	beforeCards, afterCards := cardMap(before.Cards), cardMap(after.Cards)
	for id, card := range beforeCards {
		next, exists := afterCards[id]
		if !exists {
			delta.removed[id] = true
		} else if !reflect.DeepEqual(card, next) {
			delta.changed[id] = true
		}
	}
	for id := range afterCards {
		if _, exists := beforeCards[id]; !exists {
			delta.added[id] = true
		}
	}
	delta.report = !reflect.DeepEqual(before.Report, after.Report)
	delta.layout = !reflect.DeepEqual(before.Layout, after.Layout)
	delta.filters = !reflect.DeepEqual(before.GlobalFilters, after.GlobalFilters)
	delta.extensions = !reflect.DeepEqual(before.Extensions, after.Extensions)
	return delta
}

func validateCardChangeSemantics(before, after reportjson.Document, change DraftChange) error {
	if !before.IsCardDSL() || !after.IsCardDSL() {
		return fmt.Errorf("%w: 卡片 DSL 版本不能通过普通编辑操作改变", ErrInvalidPatch)
	}
	delta := calculateCardDelta(before, after)
	targetID := change.Target.CardID
	added, removed, changed := keys(delta.added), keys(delta.removed), keys(delta.changed)
	cardOnly := !delta.report && !delta.layout && !delta.filters && !delta.extensions
	switch change.OperationType {
	case "REPORT_SETTINGS_UPDATE":
		if !delta.report || delta.layout || delta.filters || delta.extensions || len(added)+len(removed)+len(changed) > 0 {
			return cardSemanticError(change.OperationType)
		}
	case "FILTER_UPDATE":
		if !delta.filters || delta.report || delta.layout || delta.extensions || len(added)+len(removed)+len(changed) > 0 {
			return cardSemanticError(change.OperationType)
		}
	case "CARD_CREATE":
		if !cardOnly || targetID == "" || len(added) != 1 || added[0] != targetID || len(removed)+len(changed) > 0 {
			return cardSemanticError(change.OperationType)
		}
	case "CARD_DELETE":
		if !cardOnly || targetID == "" || len(removed) != 1 || removed[0] != targetID || len(added)+len(changed) > 0 {
			return cardSemanticError(change.OperationType)
		}
	case "CARD_LAYOUT_UPDATE":
		if !cardOnly || targetID == "" || len(changed) != 1 || changed[0] != targetID || len(added)+len(removed) > 0 {
			return cardSemanticError(change.OperationType)
		}
		oldCard, nextCard := cardMap(before.Cards)[targetID], cardMap(after.Cards)[targetID]
		oldCard.Layout, nextCard.Layout = nil, nil
		if !reflect.DeepEqual(oldCard, nextCard) {
			return cardSemanticError(change.OperationType)
		}
	case "CARD_CONFIG_UPDATE":
		if !cardOnly || targetID == "" || len(changed) != 1 || changed[0] != targetID || len(added)+len(removed) > 0 {
			return cardSemanticError(change.OperationType)
		}
		oldCard, nextCard := cardMap(before.Cards)[targetID], cardMap(after.Cards)[targetID]
		if !reflect.DeepEqual(oldCard.Layout, nextCard.Layout) || oldCard.ID != nextCard.ID || oldCard.Type != nextCard.Type || oldCard.CardVersion != nextCard.CardVersion {
			return cardSemanticError(change.OperationType)
		}
	case "UNDO", "REDO":
		if change.Target.ReferencedOperationID == "" || !matchesAnyCardSemanticOperation(before, after, change.Target) {
			return cardSemanticError(change.OperationType)
		}
	case "LEGACY_DRAFT_RECOVERY":
		if change.Target.CardID != "" || change.Target.FilterID != "" || change.Target.ReferencedOperationID != "" {
			return cardSemanticError(change.OperationType)
		}
	default:
		return fmt.Errorf("%w: operationType 不受卡片式报告支持", ErrInvalidRequest)
	}
	return nil
}

func matchesAnyCardSemanticOperation(before, after reportjson.Document, target ChangeTarget) bool {
	delta := calculateCardDelta(before, after)
	cardID := target.CardID
	if cardID == "" {
		for id := range delta.added {
			cardID = id
		}
		for id := range delta.removed {
			cardID = id
		}
		for id := range delta.changed {
			cardID = id
		}
	}
	for _, operationType := range []string{"REPORT_SETTINGS_UPDATE", "FILTER_UPDATE", "CARD_CREATE", "CARD_DELETE", "CARD_LAYOUT_UPDATE", "CARD_CONFIG_UPDATE"} {
		candidate := DraftChange{OperationType: operationType, Target: ChangeTarget{CardID: cardID, FilterID: target.FilterID}}
		if validateCardChangeSemantics(before, after, candidate) == nil {
			return true
		}
	}
	return false
}

func touchedCards(before, after reportjson.Document) []string {
	delta := calculateCardDelta(before, after)
	set := map[string]bool{}
	for id := range delta.added {
		set[id] = true
	}
	for id := range delta.removed {
		set[id] = true
	}
	for id := range delta.changed {
		set[id] = true
	}
	result := keys(set)
	sort.Strings(result)
	return result
}

func cardMap(cards []reportjson.Card) map[string]reportjson.Card {
	result := make(map[string]reportjson.Card, len(cards))
	for _, card := range cards {
		result[card.ID] = card
	}
	return result
}

func keys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cardSemanticError(operationType string) error {
	return fmt.Errorf("%w: %s 与实际卡片 DSL 变更不一致", ErrInvalidPatch, operationType)
}
