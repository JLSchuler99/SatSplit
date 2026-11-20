package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"satsplit/internal/lightning"
	"satsplit/internal/store"
	"satsplit/internal/vision"
)

func TestUploadRejectsBadInputWithSentences(t *testing.T) {
	s, _ := newTestServer(t)
	jpeg := encodeJPEG(t)

	t.Run("not a photo", func(t *testing.T) {
		rec := postReceipt(t, s, map[string]string{"lightning_address": "host@pay.example"}, []byte("not a photo"))
		assertError(t, rec, http.StatusBadRequest, msgNotImage)
	})
	t.Run("lightning address", func(t *testing.T) {
		rec := postReceipt(t, s, map[string]string{"lightning_address": "not-an-address"}, jpeg)
		assertError(t, rec, http.StatusBadRequest, msgBadAddress)
	})
	t.Run("missing photo", func(t *testing.T) {
		rec := postReceipt(t, s, map[string]string{"lightning_address": "host@pay.example"}, nil)
		assertError(t, rec, http.StatusBadRequest, msgMissingFile)
	})
	t.Run("too large", func(t *testing.T) {
		orig := maxReceiptSize
		maxReceiptSize = 32
		t.Cleanup(func() { maxReceiptSize = orig })
		rec := postReceipt(t, s, map[string]string{"lightning_address": "host@pay.example"}, jpeg)
		assertError(t, rec, http.StatusRequestEntityTooLarge, msgTooLarge)
	})
}

func TestUploadVisionOutcomes(t *testing.T) {
	jpeg := encodeJPEG(t)
	cases := []struct {
		name     string
		ext      vision.Extraction
		err      error
		want     string
		wantItem string
	}{
		{
			name: "no items",
			ext:  vision.Extraction{Model: "gpt-5.5"},
			err:  vision.ErrNoItems,
			want: msgNoItems,
		},
		{
			name: "timed out",
			ext:  vision.Extraction{Model: "gpt-5.5"},
			err:  fmt.Errorf("vision: request failed: Post \"https://api.openai.com/v1/chat/completions\": %w", context.DeadlineExceeded),
			want: msgReadTimeout,
		},
		{
			name: "reader unreachable",
			ext:  vision.Extraction{Model: "gpt-5.5"},
			err:  fmt.Errorf("vision: request failed: Post \"https://api.openai.com/v1/chat/completions\": dial tcp: no such host"),
			want: msgReaderDown,
		},
		{
			name: "items do not match the total",
			ext: vision.Extraction{
				Model: "gpt-5.5", Verifiable: true, Balanced: false, DiffCents: -10000,
				Items: []store.Item{{Label: "Taco", AmountCents: 500, SplitCount: 1, Quantity: 1}},
			},
			want:     "The items we read add up to $100.00 less than the printed total. Compare them with the photo and correct anything that's wrong before you share.",
			wantItem: "Taco",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestServer(t)
			t.Cleanup(func() { s.readWG.Wait() })
			v := &scriptVision{ext: tt.ext, err: tt.err}
			s.vision = v
			rec := postReceipt(t, s, map[string]string{
				"lightning_address": "host@pay.example",
				"upload_id":         "upload-" + strings.ReplaceAll(tt.name, " ", "-") + "-0001",
			}, jpeg)
			if rec.Code != http.StatusOK {
				t.Fatalf("upload status = %d: %s", rec.Code, rec.Body.String())
			}
			var created receiptResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			if !created.Refining || created.ReadMessage != "" || len(created.Items) != 0 {
				t.Fatalf("upload returned before reading finished: refining=%v message=%q items=%d", created.Refining, created.ReadMessage, len(created.Items))
			}
			if strings.Contains(rec.Body.String(), "gpt-5.5") || strings.Contains(rec.Body.String(), "openai") {
				t.Fatalf("upload response names the model:\n%s", rec.Body.String())
			}

			got := waitRead(t, s, created.Token)
			if tt.wantItem == "" {
				if got.Receipt.ItemsStatus != store.ItemsFailed || len(got.Items) != 0 {
					t.Fatalf("status=%s items=%d", got.Receipt.ItemsStatus, len(got.Items))
				}
			} else if len(got.Items) != 1 || got.Items[0].Label != tt.wantItem {
				t.Fatalf("items = %+v", got.Items)
			}

			body := getReceipt(t, s, created.Token)
			var detail receiptDetailResponse
			if err := json.Unmarshal([]byte(body), &detail); err != nil {
				t.Fatal(err)
			}
			if detail.ReadMessage != tt.want {
				t.Fatalf("read_message = %q\nwant         %q", detail.ReadMessage, tt.want)
			}
			assertUserFacing(t, detail.ReadMessage)
			if strings.Contains(body, "gpt-5.5") || strings.Contains(body, "openai") || strings.Contains(body, "no such host") {
				t.Fatalf("receipt response leaks reader details:\n%s", body)
			}
			if v.calls.Load() != 1 {
				t.Fatalf("vision calls = %d", v.calls.Load())
			}
		})
	}
}

func TestUploadDoesNotWaitOnVisionOrDuplicate(t *testing.T) {
	s, _ := newTestServer(t)
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	v := &gateVision{
		started: make(chan struct{}),
		release: release,
		ext: vision.Extraction{
			Items: []store.Item{{Label: "Soup", AmountCents: 800, SplitCount: 1, Quantity: 1}},
		},
	}
	s.vision = v
	t.Cleanup(func() {
		open()
		s.readWG.Wait()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := receiptRequest(t, ctx, map[string]string{
		"lightning_address": "host@pay.example",
		"upload_id":         "same-upload-id-0001",
	}, encodeJPEG(t))
	firstRec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.Routes().ServeHTTP(firstRec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("upload stayed open while the receipt was still being read")
	}
	first := firstRec
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", first.Code, first.Body.String())
	}
	var created receiptResponse
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	select {
	case <-v.started:
	case <-time.After(2 * time.Second):
		t.Fatal("vision did not start")
	}
	// The browser (or a proxy) has given up. Reading must still finish.
	cancel()
	time.Sleep(30 * time.Millisecond)

	second := postReceipt(t, s, map[string]string{
		"lightning_address": "host@pay.example",
		"upload_id":         "same-upload-id-0001",
	}, encodeJPEG(t))
	if second.Code != http.StatusOK {
		t.Fatalf("retry status = %d: %s", second.Code, second.Body.String())
	}
	var again receiptResponse
	if err := json.Unmarshal(second.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.Token != created.Token {
		t.Fatalf("retry token = %s, first = %s", again.Token, created.Token)
	}
	if v.calls.Load() != 1 {
		t.Fatalf("vision calls = %d, want 1", v.calls.Load())
	}

	open()
	got := waitRead(t, s, created.Token)
	if got.Receipt.ItemsStatus != store.ItemsRefined || len(got.Items) != 1 || got.Items[0].Label != "Soup" {
		t.Fatalf("after the client disconnected: status=%s items=%+v issue=%q", got.Receipt.ItemsStatus, got.Items, got.Receipt.ReadIssue)
	}
}

func TestInvoiceLightningLookupMessage(t *testing.T) {
	s, st := newTestServer(t)
	_, items := createReceipt(t, st, "token-ln",
		store.Item{Label: "Taco", AmountCents: 1000, SplitCount: 1, Quantity: 1},
	)
	dns := fmt.Errorf("Get \"https://pay.example/.well-known/lnurlp/host\": %w", &net.DNSError{
		Err: "no such host", Name: "pay.example", IsNotFound: true,
	})
	s.lnurl = errLNURL{fetchErr: dns}

	raw, err := json.Marshal(map[string]any{
		"participant": "Ana",
		"item_ids":    []int64{items[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/receipt/token-ln/selection", bytes.NewReader(raw)))
	assertError(t, rec, http.StatusBadGateway, msgLNUnreachable)
}

type scriptVision struct {
	ext   vision.Extraction
	err   error
	calls atomic.Int32
}

func (s *scriptVision) ExtractItems(context.Context, string, vision.Hint) (vision.Extraction, error) {
	s.calls.Add(1)
	return s.ext, s.err
}

// gateVision blocks until release is closed, and reports the context it was
// given so a canceled upload can be told apart from the read's own deadline.
type gateVision struct {
	started chan struct{}
	release chan struct{}
	ext     vision.Extraction
	calls   atomic.Int32
}

func (g *gateVision) ExtractItems(ctx context.Context, _ string, _ vision.Hint) (vision.Extraction, error) {
	if g.calls.Add(1) == 1 {
		close(g.started)
	}
	select {
	case <-ctx.Done():
		return vision.Extraction{Model: "gpt-5.5"}, ctx.Err()
	default:
	}
	select {
	case <-g.release:
		return g.ext, nil
	case <-ctx.Done():
		return vision.Extraction{Model: "gpt-5.5"}, ctx.Err()
	}
}

type errLNURL struct {
	fetchErr error
}

func (e errLNURL) FetchLNURLPay(context.Context, string) (lightning.LNURLPayResponse, error) {
	return lightning.LNURLPayResponse{}, e.fetchErr
}

func (errLNURL) RequestInvoice(context.Context, lightning.LNURLPayResponse, int64, string) (lightning.Invoice, error) {
	return lightning.Invoice{}, errors.New("not called")
}

func postReceipt(t *testing.T, s *Server, fields map[string]string, file []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := receiptRequest(t, context.Background(), fields, file)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

func receiptRequest(t *testing.T, ctx context.Context, fields map[string]string, file []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if file != nil {
		fw, err := mw.CreateFormFile("receipt", "receipt.jpg")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(file); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/payer/receipt", &body).WithContext(ctx)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func getReceipt(t *testing.T, s *Server, token string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/receipt/"+token, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func waitRead(t *testing.T, s *Server, token string) store.ReceiptWithItems {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last store.ReceiptWithItems
	for time.Now().Before(deadline) {
		got, err := s.store.GetReceiptByToken(token)
		if err == nil {
			last = got
			if got.Receipt.ItemsStatus != store.ItemsOCR {
				s.readWG.Wait()
				return got
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("read did not finish, status %q issue %q", last.Receipt.ItemsStatus, last.Receipt.ReadIssue)
	return last
}

func assertError(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	var payload errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error != msg {
		t.Fatalf("error = %q\nwant  %q", payload.Error, msg)
	}
	assertUserFacing(t, payload.Error)
}
