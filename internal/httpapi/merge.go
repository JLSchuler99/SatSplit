package httpapi

import (
	"strings"

	"satsplit/internal/store"
)

// mergeDuplicateItems folds lines with the same name and the same unit price
// into one line with a larger quantity. A receipt rung up seat by seat lists
// the same drink many times; one row with a unit picker is easier to claim
// from. The total of the amounts never changes. Lines keep the position of
// their first occurrence. A line whose total does not divide evenly by its
// quantity is left alone, because its unit price is not known exactly.
func mergeDuplicateItems(items []store.Item) []store.Item {
	type key struct {
		label     string
		unitCents int64
	}
	merged := make([]store.Item, 0, len(items))
	index := make(map[key]int, len(items))
	for _, item := range items {
		quantity := item.Quantity
		if quantity < 1 {
			quantity = 1
		}
		item.Quantity = quantity
		label := strings.ToUpper(strings.Join(strings.Fields(item.Label), " "))
		if label == "" || item.AmountCents <= 0 || item.AmountCents%quantity != 0 || item.SplitCount > 1 {
			merged = append(merged, item)
			continue
		}
		k := key{label: label, unitCents: item.AmountCents / quantity}
		at, seen := index[k]
		if !seen || merged[at].Quantity+quantity > maxItemQuantity {
			index[k] = len(merged)
			merged = append(merged, item)
			continue
		}
		merged[at].Quantity += quantity
		merged[at].AmountCents += item.AmountCents
		if merged[at].TranslatedLabel == "" {
			merged[at].TranslatedLabel = item.TranslatedLabel
		}
	}
	return merged
}
