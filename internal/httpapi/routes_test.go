package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"satsplit/internal/lightning"
	"satsplit/internal/store"
	"satsplit/internal/store/sqlite"
)

func TestServeFont(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantFont bool
	}{
		{"vendored font", "/fonts/inter-latin-var.woff2", true},
		{"second font", "/fonts/jetbrains-mono-latin-500.woff2", true},
		{"unknown font", "/fonts/missing.woff2", false},
		{"not a woff2", "/fonts/OFL-Inter.txt", false},
		{"parent directory", "/fonts/../app.js", false},
		{"nested path", "/fonts/sub/inter-latin-var.woff2", false},
		{"encoded slash", "/fonts/..%2fapp.js", false},
		{"empty name", "/fonts/.woff2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// serveStatic is called directly so the mux does not clean the
			// path first; the font route must hold up on its own.
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.URL.Path = tt.path
			rec := httptest.NewRecorder()
			serveStatic(rec, req)

			if !tt.wantFont {
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404", rec.Code)
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != "font/woff2" {
				t.Fatalf("content type = %q", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Fatalf("cache control = %q", got)
			}
			if !bytes.HasPrefix(rec.Body.Bytes(), []byte("wOF2")) {
				t.Fatalf("body is not a woff2 file")
			}
		})
	}
}

func newTestServer(t *testing.T) (*Server, *sqlite.Store) {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "satsplit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewServer(Config{Store: st, Rates: fixedRate(50000), UploadDir: t.TempDir()}), st
}

func createReceipt(t *testing.T, st *sqlite.Store, token string, items ...store.Item) (store.Receipt, []store.Item) {
	t.Helper()
	receipt, err := st.CreateReceipt(store.Receipt{
		Token:            token,
		LightningAddress: "host@pay.example",
		ReceiptFilename:  "receipt.jpg",
		CurrencyCode:     "USD",
		CreatedAt:        time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		return receipt, nil
	}
	saved, err := st.ReplaceReceiptItems(receipt.ID, items)
	if err != nil {
		t.Fatal(err)
	}
	return receipt, saved
}

func TestSelectionQR(t *testing.T) {
	s, st := newTestServer(t)
	receiptA, _ := createReceipt(t, st, "token-a")
	receiptB, _ := createReceipt(t, st, "token-b")

	const invoiceA = "lnbc10n1pjtestinvoicea"
	selA, err := st.CreateSelection(store.Selection{
		ReceiptID: receiptA.ID, Participant: "Ana", TotalCents: 500, Invoice: invoiceA, CreatedAt: time.Now().UTC(),
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	selB, err := st.CreateSelection(store.Selection{
		ReceiptID: receiptB.ID, Participant: "Ben", TotalCents: 700, Invoice: "lnbc10n1pjtestinvoiceb", CreatedAt: time.Now().UTC(),
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	wantPNG, err := qrcode.Encode("LIGHTNING:LNBC10N1PJTESTINVOICEA", qrcode.Medium, 320)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"own selection", "/api/receipt/token-a/selections/" + itoa(selA.ID) + "/qr", http.StatusOK},
		{"selection of another receipt", "/api/receipt/token-a/selections/" + itoa(selB.ID) + "/qr", http.StatusNotFound},
		{"unknown selection", "/api/receipt/token-a/selections/99999/qr", http.StatusNotFound},
		{"non-numeric id", "/api/receipt/token-a/selections/abc/qr", http.StatusNotFound},
		{"unknown receipt", "/api/receipt/nope/selections/" + itoa(selA.ID) + "/qr", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			if got := rec.Header().Get("Content-Type"); got != "image/png" {
				t.Fatalf("content type = %q", got)
			}
			cfg, _, err := image.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
			if err != nil {
				t.Fatalf("not a png: %v", err)
			}
			if cfg.Width != 320 || cfg.Height != 320 {
				t.Fatalf("size = %dx%d", cfg.Width, cfg.Height)
			}
			if !bytes.Equal(rec.Body.Bytes(), wantPNG) {
				t.Fatalf("QR does not encode this selection's invoice as an upper-case lightning: URI")
			}
		})
	}
}

// TestSelectionInvoiceID checks that the response names the stored row of
// the invoice it returns, including the second invoice made after paying,
// where selection_id stays the paid selection.
func TestSelectionInvoiceID(t *testing.T) {
	s, st := newTestServer(t)
	_, items := createReceipt(t, st, "token-pay",
		store.Item{Label: "Tacos", AmountCents: 1000, SplitCount: 1, Quantity: 1},
		store.Item{Label: "Flan", AmountCents: 500, SplitCount: 1, Quantity: 1},
	)

	invoices := []string{"lnbc1firstinvoice", "lnbc1secondinvoice"}
	calls := 0
	stubLNURL(t, func() string {
		inv := invoices[calls]
		calls++
		return inv
	})

	first := postSelection(t, s, map[string]any{
		"participant": "Ana",
		"item_ids":    []int64{items[0].ID},
	})
	if first.InvoiceSelectionID == 0 || first.InvoiceSelectionID != first.SelectionID {
		t.Fatalf("first invoice: selection_id = %d, invoice_selection_id = %d", first.SelectionID, first.InvoiceSelectionID)
	}

	if err := st.MarkSelectionPaid(first.SelectionID, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	second := postSelection(t, s, map[string]any{
		"participant":          "Ana",
		"item_ids":             []int64{items[0].ID, items[1].ID},
		"replace_selection_id": first.SelectionID,
		"edit_secret":          first.EditSecret,
	})
	if second.SelectionID != first.SelectionID {
		t.Fatalf("second invoice selection_id = %d, want the paid selection %d", second.SelectionID, first.SelectionID)
	}
	if second.InvoiceSelectionID == first.SelectionID {
		t.Fatalf("invoice_selection_id points at the paid selection")
	}

	selections, err := st.ListSelections(items[0].ReceiptID)
	if err != nil {
		t.Fatal(err)
	}
	var found *store.SelectionSummary
	for i := range selections {
		if selections[i].ID == second.InvoiceSelectionID {
			found = &selections[i]
		}
	}
	if found == nil {
		t.Fatalf("no stored selection %d", second.InvoiceSelectionID)
	}
	if found.Invoice != second.Invoice || found.Invoice != "lnbc1secondinvoice" {
		t.Fatalf("stored invoice = %q, response invoice = %q", found.Invoice, second.Invoice)
	}
	if found.ParentID == nil || *found.ParentID != first.SelectionID {
		t.Fatalf("second invoice parent = %v", found.ParentID)
	}
}

type fixedRate float64

func (r fixedRate) BTCCurrencyPrice(context.Context, string) (float64, error) {
	return float64(r), nil
}

func stubLNURL(t *testing.T, nextInvoice func() string) {
	t.Helper()
	origFetch, origRequest := fetchLNURLPay, requestInvoice
	t.Cleanup(func() { fetchLNURLPay, requestInvoice = origFetch, origRequest })
	fetchLNURLPay = func(context.Context, string) (lightning.LNURLPayResponse, error) {
		return lightning.LNURLPayResponse{Callback: "https://pay.example/cb", MinSendable: 1000, MaxSendable: 1_000_000_000}, nil
	}
	requestInvoice = func(context.Context, lightning.LNURLPayResponse, int64, string) (lightning.Invoice, error) {
		return lightning.Invoice{PaymentRequest: nextInvoice()}, nil
	}
}

func postSelection(t *testing.T, s *Server, body map[string]any) selectionResponse {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/receipt/token-pay/selection", bytes.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var resp selectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
