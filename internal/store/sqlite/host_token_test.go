package sqlite

import (
	"testing"
	"time"

	"satsplit/internal/store"
)

func TestReceiptFoundByHostOrGuestToken(t *testing.T) {
	s := testStore(t)
	created, err := s.CreateReceipt(store.Receipt{
		Token:            "guest-token",
		HostToken:        "host-token",
		LightningAddress: "alice@pay.example",
		ReceiptFilename:  "receipt.jpg",
		CurrencyCode:     "USD",
		CreatedAt:        time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"guest-token", "host-token"} {
		got, err := s.GetReceiptByToken(token)
		if err != nil {
			t.Fatalf("lookup by %q: %v", token, err)
		}
		if got.Receipt.ID != created.ID || got.Receipt.Token != "guest-token" || got.Receipt.HostToken != "host-token" {
			t.Fatalf("lookup by %q returned %+v", token, got.Receipt)
		}
	}
	if _, err := s.GetReceiptByToken("someone-else"); err == nil {
		t.Fatal("unknown token found a receipt")
	}
}

// A receipt without a host token of its own, as created before host tokens
// existed, uses its one token for both roles.
func TestLegacyReceiptSharesOneToken(t *testing.T) {
	s := testStore(t)
	if _, err := s.CreateReceipt(store.Receipt{
		Token:            "shared-token",
		LightningAddress: "alice@pay.example",
		ReceiptFilename:  "receipt.jpg",
		CurrencyCode:     "USD",
		CreatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReceiptByToken("shared-token")
	if err != nil {
		t.Fatal(err)
	}
	if got.Receipt.HostToken != "shared-token" {
		t.Fatalf("HostToken = %q, want the shared token", got.Receipt.HostToken)
	}
}
