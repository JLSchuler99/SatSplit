package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"satsplit/internal/store"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "satsplit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testReceipt(t *testing.T, s *Store) store.Receipt {
	t.Helper()
	receipt, err := s.CreateReceipt(store.Receipt{
		Token:            "token-1",
		LightningAddress: "alice@pay.example",
		ReceiptFilename:  "receipt.jpg",
		ImagePath:        "receipt.jpg",
		CurrencyCode:     "USD",
		CreatedAt:        time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ItemsStatus != store.ItemsOCR {
		t.Fatalf("status = %q", receipt.ItemsStatus)
	}
	return receipt
}

func TestReplaceAutoItemsStopsAfterManualEdit(t *testing.T) {
	s := testStore(t)
	receipt := testReceipt(t, s)
	if _, err := s.ReplaceReceiptItems(receipt.ID, []store.Item{{
		Label: "Latte", AmountCents: 450, SplitCount: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkItemsManual(receipt.ID); err != nil {
		t.Fatal(err)
	}
	replaced, _, err := s.ReplaceAutoItems(receipt.ID, []store.Item{{
		Label: "Vision Latte", AmountCents: 500, SplitCount: 1,
	}}, store.ItemsRefined)
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("vision replace crossed a manual edit")
	}
	got, err := s.GetReceiptByToken(receipt.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Receipt.ItemsStatus != store.ItemsManual || len(got.Items) != 1 || got.Items[0].Label != "Latte" {
		t.Fatalf("receipt = %+v items = %+v", got.Receipt, got.Items)
	}
}

func TestReplaceAutoItemsStopsAfterSelection(t *testing.T) {
	s := testStore(t)
	receipt := testReceipt(t, s)
	items, err := s.ReplaceReceiptItems(receipt.ID, []store.Item{{
		Label: "Latte", AmountCents: 450, SplitCount: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSelection(store.Selection{
		ReceiptID:   receipt.ID,
		Participant: "bob",
		TotalCents:  450,
		Invoice:     "lnbc1example",
		CreatedAt:   time.Now().UTC(),
	}, []int64{items[0].ID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	replaced, _, err := s.ReplaceAutoItems(receipt.ID, []store.Item{{
		Label: "Other", AmountCents: 100, SplitCount: 1,
	}}, store.ItemsRefined)
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("vision replace deleted items a guest had selected")
	}
}

func TestReplaceAutoItemsAppliesToDraft(t *testing.T) {
	s := testStore(t)
	receipt := testReceipt(t, s)
	if _, err := s.ReplaceReceiptItems(receipt.ID, []store.Item{{
		Label: "OCR", AmountCents: 100, SplitCount: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	replaced, items, err := s.ReplaceAutoItems(receipt.ID, []store.Item{{
		Label: "Refined", AmountCents: 250, SplitCount: 1, TranslatedLabel: "Refined",
	}}, store.ItemsRefined)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced || len(items) != 1 || items[0].ID == 0 || items[0].Label != "Refined" {
		t.Fatalf("replaced=%v items=%+v", replaced, items)
	}
	got, err := s.GetReceiptByToken(receipt.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Receipt.ItemsStatus != store.ItemsRefined || got.Items[0].AmountCents != 250 {
		t.Fatalf("got = %+v %+v", got.Receipt, got.Items)
	}
}

func TestUploadIDIsStoredOnce(t *testing.T) {
	s := testStore(t)
	const uploadID = "upload-id-12345678"
	first, err := s.CreateReceipt(store.Receipt{
		Token: "guest-a", HostToken: "host-a", LightningAddress: "a@pay.example",
		ReceiptFilename: "r.jpg", CurrencyCode: "USD", UploadID: uploadID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateReceipt(store.Receipt{
		Token: "guest-b", HostToken: "host-b", LightningAddress: "a@pay.example",
		ReceiptFilename: "r.jpg", CurrencyCode: "USD", UploadID: uploadID, CreatedAt: time.Now().UTC(),
	})
	if !errors.Is(err, store.ErrUploadExists) {
		t.Fatalf("second insert err = %v, want ErrUploadExists", err)
	}
	got, err := s.GetReceiptByUploadID(uploadID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Receipt.ID != first.ID || got.Receipt.Token != "guest-a" {
		t.Fatalf("lookup = %+v", got.Receipt)
	}
	// Receipts with no client id still insert independently.
	if _, err := s.CreateReceipt(store.Receipt{
		Token: "guest-c", LightningAddress: "a@pay.example", ReceiptFilename: "r.jpg", CurrencyCode: "USD", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateReceipt(store.Receipt{
		Token: "guest-d", LightningAddress: "a@pay.example", ReceiptFilename: "r.jpg", CurrencyCode: "USD", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSetReadStateStopsAfterEdit(t *testing.T) {
	s := testStore(t)
	receipt := testReceipt(t, s)
	if err := s.SetReadState(receipt.ID, "", true, -10000); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReceiptByToken(receipt.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Receipt.BalanceChecked || got.Receipt.BalanceDiffCents != -10000 {
		t.Fatalf("balance = checked %v diff %d", got.Receipt.BalanceChecked, got.Receipt.BalanceDiffCents)
	}
	if err := s.MarkItemsManual(receipt.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReadState(receipt.ID, store.ReadNoItems, true, 50); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetReceiptByToken(receipt.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Receipt.ItemsStatus != store.ItemsManual || got.Receipt.ReadIssue != "" || got.Receipt.BalanceChecked || got.Receipt.BalanceDiffCents != 0 {
		t.Fatalf("after edit: %+v", got.Receipt)
	}
}
