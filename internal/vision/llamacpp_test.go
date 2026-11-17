package vision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"satsplit/internal/store"
)

func TestParseItemsAmounts(t *testing.T) {
	raw := "```json\nHere is the receipt:\n{\"items\":[" +
		"{\"label\":\"Latte\",\"label_en\":\"Latte\",\"amount\":\"4.50\"}," +
		"{\"label\":\"Tea\",\"amount\":3.5}," +
		"{\"name\":\"Water\",\"amount_cents\":200}," +
		"{\"label\":\"Tax\",\"amount\":\"0\"}" +
		"]}\n```"
	got, err := parseItems(raw, "USD")
	if err != nil {
		t.Fatal(err)
	}
	items := got.Items
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].AmountCents != 450 || items[1].AmountCents != 350 || items[2].Label != "Water" || items[2].AmountCents != 200 {
		t.Fatalf("items = %+v", items)
	}
}

func TestParseItemsTaxIsNotALineItem(t *testing.T) {
	raw := `{"items":[{"label":"Mofongo","amount":"500.00"},{"label":"ITBIS","amount":"90.00"},{"label":"10% Ley","amount":"50.00"}],"tax":"140.00"}`
	got, err := parseItems(raw, "DOP")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Label != "Mofongo" {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.TaxCents != 14000 {
		t.Fatalf("tax = %d", got.TaxCents)
	}
}

func TestParseItemsEuroComma(t *testing.T) {
	got, err := parseItems(`[{"label":"Cafe","amount":"3,50"}]`, "EUR")
	if err != nil {
		t.Fatal(err)
	}
	items := got.Items
	if len(items) != 1 || items[0].AmountCents != 350 {
		t.Fatalf("items = %+v", items)
	}
}

func TestParseItemsSubtotalAndTotal(t *testing.T) {
	raw := `{"items":[{"label":"Mofongo","amount":"500.00"}],"tax":"140.00","subtotal":"500.00","total":"640.00"}`
	got, err := parseItems(raw, "DOP")
	if err != nil {
		t.Fatal(err)
	}
	if got.SubtotalCents != 50000 || got.TotalCents != 64000 {
		t.Fatalf("subtotal = %d, total = %d", got.SubtotalCents, got.TotalCents)
	}
}

func TestBalance(t *testing.T) {
	items := func(cents ...int64) []store.Item {
		var out []store.Item
		for _, c := range cents {
			out = append(out, store.Item{Label: "x", AmountCents: c})
		}
		return out
	}
	tests := []struct {
		name       string
		ext        Extraction
		target     int64
		diff       int64
		verifiable bool
	}{
		{"matches subtotal", Extraction{Items: items(42000, 44000), SubtotalCents: 86000}, 86000, 0, true},
		{"over subtotal", Extraction{Items: items(42000, 44000, 9000), SubtotalCents: 86000, TotalCents: 100000, TaxCents: 14000}, 86000, 9000, true},
		{"matches total less tax", Extraction{Items: items(50000), TotalCents: 64000, TaxCents: 14000}, 50000, 0, true},
		{"tax-inclusive prices match total", Extraction{Items: items(1190), TotalCents: 1190, TaxCents: 190}, 1190, 0, true},
		{"one cent rounding", Extraction{Items: items(999), SubtotalCents: 1000}, 1000, 0, true},
		{"nothing printed", Extraction{Items: items(500)}, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, diff, verifiable := tt.ext.balance()
			if target != tt.target || diff != tt.diff || verifiable != tt.verifiable {
				t.Fatalf("balance() = %d, %d, %v; want %d, %d, %v", target, diff, verifiable, tt.target, tt.diff, tt.verifiable)
			}
		})
	}
}

func TestLearnQuirk(t *testing.T) {
	var q quirks
	if !learnQuirk(&q, `Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.`) || !q.maxCompletionTokens {
		t.Fatalf("max_tokens not learned: %+v", q)
	}
	if !learnQuirk(&q, `Unsupported value: 'temperature' does not support 0 with this model.`) || !q.noTemperature {
		t.Fatalf("temperature not learned: %+v", q)
	}
	if !learnQuirk(&q, `response_format is not supported`) || !q.noResponseFormat {
		t.Fatalf("response_format not learned: %+v", q)
	}
	if learnQuirk(&q, `some other bad request`) {
		t.Fatal("expected nothing left to change")
	}
}

// TestExtractItemsRetriesUntilBalanced serves a reply whose items do not add
// up, then a corrected one, and checks the second is accepted.
func TestExtractItemsRetriesUntilBalanced(t *testing.T) {
	replies := []string{
		`{"items":[{"label":"Limonada","amount":"480.00"},{"label":"Expresso","amount":"90.00"},{"label":"Expresso","amount":"90.00"}],"tax":"0.00","subtotal":"570.00","total":""}`,
		`{"items":[{"label":"Limonada","amount":"480.00"},{"label":"Expresso","amount":"90.00"}],"tax":"0.00","subtotal":"570.00","total":""}`,
	}
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if strings.Contains(string(b), `"max_tokens"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported parameter: 'max_tokens'. Use 'max_completion_tokens' instead."}}`))
			return
		}
		reply := replies[0]
		replies = replies[1:]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": reply}}},
			"usage":   map[string]int{"prompt_tokens": 100, "completion_tokens": 20},
		})
	}))
	defer srv.Close()

	img := filepath.Join(t.TempDir(), "r.jpg")
	if err := os.WriteFile(img, []byte("not really a jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	got, err := c.ExtractItems(context.Background(), img, Hint{Currency: "DOP"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Balanced || len(got.Items) != 2 || len(got.Attempts) != 2 {
		t.Fatalf("balanced=%v items=%d attempts=%d", got.Balanced, len(got.Items), len(got.Attempts))
	}
	if got.Attempts[0].Balanced || got.Attempts[0].DiffCents != 9000 {
		t.Fatalf("first attempt = %+v", got.Attempts[0])
	}
	// One rejected request, then two accepted ones; the quirk is remembered.
	if len(bodies) != 3 {
		t.Fatalf("requests = %d", len(bodies))
	}
	if !strings.Contains(bodies[2], "add up to 660.00") || !strings.Contains(bodies[2], "570.00") {
		t.Fatalf("feedback missing from retry: %s", bodies[2])
	}
	if strings.Contains(bodies[2], `"max_tokens"`) || !strings.Contains(bodies[2], `"max_completion_tokens"`) {
		t.Fatalf("quirk not remembered: %s", bodies[2])
	}
}

func TestParseItemsQuantity(t *testing.T) {
	raw := `{"items":[` +
		`{"label":"Aloha Salmon","quantity":2,"amount":"1730.00"},` +
		`{"label":"Ice Te","quantity":"2","amount":"298.00"},` +
		`{"label":"Te Verde","amount":"120.00"},` +
		`{"label":"Odd","quantity":1.5,"amount":"10.00"}` +
		`],"tax":"0.00","subtotal":"2158.00","total":""}`
	got, err := parseItems(raw, "DOP")
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{2, 2, 1, 1}
	if len(got.Items) != len(want) {
		t.Fatalf("items = %+v", got.Items)
	}
	for i, q := range want {
		if got.Items[i].Quantity != q {
			t.Fatalf("item %d quantity = %d, want %d", i, got.Items[i].Quantity, q)
		}
	}
	if got.Items[0].AmountCents != 173000 {
		t.Fatalf("line total = %d", got.Items[0].AmountCents)
	}
}

type stubService struct {
	ext   Extraction
	err   error
	calls int
}

func (s *stubService) ExtractItems(context.Context, string, Hint) (Extraction, error) {
	s.calls++
	return s.ext, s.err
}

func TestWithFallback(t *testing.T) {
	balanced := Extraction{Model: "good", Verifiable: true, Balanced: true, Attempts: []Attempt{{Model: "good"}}}
	off := Extraction{Model: "cheap", Verifiable: true, DiffCents: 9000, Attempts: []Attempt{{Model: "cheap"}, {Model: "cheap"}}}
	unverifiable := Extraction{Model: "cheap", Attempts: []Attempt{{Model: "cheap"}}}

	tests := []struct {
		name          string
		primary       *stubService
		secondary     *stubService
		wantModel     string
		wantSecondary int
		wantAttempts  int
		wantErr       bool
	}{
		{"balanced primary is kept", &stubService{ext: balanced}, &stubService{ext: off}, "good", 0, 1, false},
		{"nothing to check is kept", &stubService{ext: unverifiable}, &stubService{ext: balanced}, "cheap", 0, 1, false},
		{"unbalanced primary escalates", &stubService{ext: off}, &stubService{ext: balanced}, "good", 1, 3, false},
		{"primary error escalates", &stubService{err: errors.New("boom")}, &stubService{ext: balanced}, "good", 1, 1, false},
		{"secondary error keeps primary", &stubService{ext: off}, &stubService{err: errors.New("boom")}, "cheap", 1, 2, false},
		{"both fail", &stubService{err: errors.New("a")}, &stubService{err: errors.New("b")}, "", 1, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WithFallback(tt.primary, tt.secondary).ExtractItems(context.Background(), "x.jpg", Hint{})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if got.Model != tt.wantModel || tt.secondary.calls != tt.wantSecondary || len(got.Attempts) != tt.wantAttempts {
				t.Fatalf("model=%q secondary calls=%d attempts=%d", got.Model, tt.secondary.calls, len(got.Attempts))
			}
		})
	}
}

func TestTaxIncludedPricesAddNoTax(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": `{"items":[{"label":"Tacos","quantity":1,"amount":"289.00"},{"label":"Nachos","quantity":1,"amount":"210.00"}],"tax":"68.83","subtotal":"499.00","total":"499.00"}`,
			}}},
		})
	}))
	defer srv.Close()
	img := filepath.Join(t.TempDir(), "r.jpg")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := NewClient(Config{BaseURL: srv.URL}).ExtractItems(context.Background(), img, Hint{Currency: "MXN"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.TaxIncluded || got.TaxCents != 0 || !got.Balanced {
		t.Fatalf("taxIncluded=%v tax=%d balanced=%v", got.TaxIncluded, got.TaxCents, got.Balanced)
	}
}

func TestMissedServiceChargeComesFromTotal(t *testing.T) {
	reply := `{"items":[{"label":"Desayuno Buffet","quantity":4,"amount":"6200.00"}],"tax":"1116.00","subtotal":"6200.00","total":"7936.00"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
		})
	}))
	defer srv.Close()

	img := filepath.Join(t.TempDir(), "r.jpg")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ext, err := NewClient(Config{BaseURL: srv.URL}).ExtractItems(context.Background(), img, Hint{Currency: "DOP"})
	if err != nil {
		t.Fatal(err)
	}
	if !ext.ChargesFromTotal || ext.TaxCents != 173600 {
		t.Fatalf("ChargesFromTotal=%v TaxCents=%d, want true and 173600", ext.ChargesFromTotal, ext.TaxCents)
	}
}
