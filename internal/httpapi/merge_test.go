package httpapi

import (
	"testing"

	"satsplit/internal/store"
)

func TestMergeDuplicateItems(t *testing.T) {
	in := []store.Item{
		{Label: "MARGHERITA", TranslatedLabel: "Margherita", AmountCents: 46000, Quantity: 1},
		{Label: "REFRESCO", TranslatedLabel: "Soft drink", AmountCents: 7000, Quantity: 1},
		{Label: "Refresco ", AmountCents: 14000, Quantity: 2},
		{Label: "REFRESCO", AmountCents: 9000, Quantity: 1}, // different unit price
		{Label: "MARGHERITA", AmountCents: 46000, Quantity: 1},
		{Label: "AGUA", AmountCents: 10000, Quantity: 3}, // unit price not exact
		{Label: "AGUA", AmountCents: 10000, Quantity: 3},
	}
	got := mergeDuplicateItems(in)
	want := []struct {
		label    string
		amount   int64
		quantity int64
	}{
		{"MARGHERITA", 92000, 2},
		{"REFRESCO", 21000, 3},
		{"REFRESCO", 9000, 1},
		{"AGUA", 10000, 3},
		{"AGUA", 10000, 3},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(got), len(want), got)
	}
	var sumIn, sumOut int64
	for _, item := range in {
		sumIn += item.AmountCents
	}
	for i, w := range want {
		sumOut += got[i].AmountCents
		if got[i].Label != w.label || got[i].AmountCents != w.amount || got[i].Quantity != w.quantity {
			t.Errorf("item %d = %q %d x%d, want %q %d x%d", i, got[i].Label, got[i].AmountCents, got[i].Quantity, w.label, w.amount, w.quantity)
		}
	}
	if sumIn != sumOut {
		t.Errorf("total changed: %d -> %d", sumIn, sumOut)
	}
	if got[1].TranslatedLabel != "Soft drink" {
		t.Errorf("translation lost: %q", got[1].TranslatedLabel)
	}
}
