package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"satsplit/internal/lightning"
	"satsplit/internal/ocr"
	"satsplit/internal/rates"
	"satsplit/internal/store"
	"satsplit/internal/translate"
	"satsplit/internal/vision"
)

const (
	maxReceiptSize = 20 << 20 // 20 MB
)

// LNURLService asks the payer's Lightning address provider for an invoice.
type LNURLService interface {
	FetchLNURLPay(ctx context.Context, address string) (lightning.LNURLPayResponse, error)
	RequestInvoice(ctx context.Context, lnurl lightning.LNURLPayResponse, amountMSat int64, comment string) (lightning.Invoice, error)
}

// liveLNURL is the LNURLService that calls the real provider.
type liveLNURL struct{}

func (liveLNURL) FetchLNURLPay(ctx context.Context, address string) (lightning.LNURLPayResponse, error) {
	return lightning.FetchLNURLPay(ctx, address)
}

func (liveLNURL) RequestInvoice(ctx context.Context, lnurl lightning.LNURLPayResponse, amountMSat int64, comment string) (lightning.Invoice, error) {
	return lightning.RequestInvoice(ctx, lnurl, amountMSat, comment)
}

type OCRService interface {
	Extract(ctx context.Context, imagePath string, req ocr.Request) (ocr.Result, error)
}

type Config struct {
	OCR             OCRService
	Vision          vision.Service
	Store           store.Store
	BaseURL         string
	FiatCentsPerSat int64
	UploadDir       string
	DefaultOCRLang  string
	Rates           rates.PriceService
	Translator      translate.Service
	// LNURL defaults to the real Lightning address provider when nil.
	LNURL LNURLService
	// VisionOCRHint runs Tesseract before the vision pass and gives its text
	// to the model as a hint. The Tesseract result is never shown or stored
	// as the item list.
	VisionOCRHint bool
}

type Server struct {
	ocr             OCRService
	vision          vision.Service
	store           store.Store
	baseURL         string
	fiatCentsPerSat int64
	uploadDir       string
	defaultOCRLang  string
	rates           rates.PriceService
	translator      translate.Service
	lnurl           LNURLService
	visionOCRHint   bool
	priceCache      map[string]cachedPrice
	priceMu         sync.Mutex
	archiveMu       sync.Mutex
	verifyMu        sync.Mutex
	verifyLast      map[int64]time.Time
}

func NewServer(cfg Config) *Server {
	centsPerSat := cfg.FiatCentsPerSat
	if centsPerSat <= 0 {
		centsPerSat = 1
	}
	uploadDir := strings.TrimSpace(cfg.UploadDir)
	if uploadDir == "" {
		uploadDir = "uploads"
	}
	lnurl := cfg.LNURL
	if lnurl == nil {
		lnurl = liveLNURL{}
	}
	return &Server{
		ocr:             cfg.OCR,
		vision:          cfg.Vision,
		store:           cfg.Store,
		baseURL:         cfg.BaseURL,
		fiatCentsPerSat: centsPerSat,
		uploadDir:       uploadDir,
		defaultOCRLang:  strings.TrimSpace(cfg.DefaultOCRLang),
		rates:           cfg.Rates,
		translator:      cfg.Translator,
		lnurl:           lnurl,
		visionOCRHint:   cfg.VisionOCRHint,
		priceCache:      make(map[string]cachedPrice),
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/payer/receipt", s.handleReceiptUpload)
	mux.HandleFunc("/api/receipt/", s.handleReceiptRoutes)
	mux.Handle("/", s.staticHandler())
	return withJSONHeaders(mux)
}

// handleConfig tells the page which upload options apply. The OCR language
// is only used by Tesseract, so the page hides it when a vision model reads
// the receipt.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]bool{"vision": s.vision != nil})
}

func (s *Server) handleReceiptUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method_not_allowed"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxReceiptSize+2<<20)
	if err := r.ParseMultipartForm(maxReceiptSize); err != nil {
		if errors.Is(err, http.ErrNotMultipart) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "not_multipart"})
			return
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) || errors.Is(err, multipart.ErrMessageTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "receipt_too_large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_multipart_form"})
		return
	}

	lightningAddress := strings.TrimSpace(r.FormValue("lightning_address"))
	if lightningAddress == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing_lightning_address"})
		return
	}
	if !lightning.ValidateAddressSyntax(lightningAddress) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_lightning_address"})
		return
	}

	file, header, err := r.FormFile("receipt")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing_receipt_file"})
		return
	}
	defer file.Close()

	// The guest token goes in the share link. The host token is returned only
	// to the uploader and is needed to edit the receipt.
	token, err := randomToken(guestTokenBytes)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_save_failed"})
		return
	}
	hostToken, err := randomToken(hostTokenBytes)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_save_failed"})
		return
	}
	receiptPath, err := s.persistReceiptToDisk(file, header, token)
	if errors.Is(err, errNotAnImage) {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "That file is not a photo SatSplit can read. Use a JPEG, PNG, WebP or HEIC image.",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_save_failed"})
		return
	}
	receipt := store.Receipt{
		Token:            token,
		HostToken:        hostToken,
		LightningAddress: lightningAddress,
		ReceiptFilename:  header.Filename,
		ImagePath:        receiptPath,
		CurrencyCode:     strings.ToUpper(strings.TrimSpace(r.FormValue("currency_code"))),
		CreatedAt:        time.Now().UTC(),
	}
	if receipt.CurrencyCode == "" {
		receipt.CurrencyCode = "USD"
	}
	receipt, err = s.store.CreateReceipt(receipt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_store_failed"})
		return
	}

	ocrLang := receiptOCRLang(r.FormValue("ocr_lang"), s.defaultOCRLang, receipt.CurrencyCode)
	if archived, err := s.keepReceiptImage(token, receiptPath, receipt.CurrencyCode, ocrLang); err != nil {
		log.Printf("archive: receipt %s: %v", token, err)
	} else {
		log.Printf("archive: saved %s", archived)
	}

	var (
		items         []store.Item
		ocrText       string
		ocrConfidence float64
	)
	if s.vision != nil {
		// With a vision model configured it is the only reader of the item
		// list, so a rough Tesseract draft is never shown or saved.
		hint := ""
		if s.visionOCRHint {
			// Hint only: this text goes to the model and the archive, never
			// to the item list or the page.
			if res, err := s.runOCR(r.Context(), receiptPath, ocrLang, receipt.CurrencyCode); err != nil {
				s.recordArchiveParse(token, "tesseract", "", "", nil, 0, 0, err)
			} else {
				s.recordArchiveParse(token, "tesseract", res.Text, res.Stats.Pass, res.Items, res.TaxCents, res.Confidence, nil)
				hint = res.Text
			}
		}
		items = s.readWithVision(r.Context(), receipt.ID, receipt.Token, receiptPath, receipt.CurrencyCode, hint)
		if fresh, err := s.store.GetReceiptByToken(receipt.Token); err == nil {
			receipt = fresh.Receipt
			items = fresh.Items
		}
		go s.translateAndSave(receipt.ID, items, "")
	} else {
		ocrResult, err := s.runOCR(r.Context(), receiptPath, ocrLang, receipt.CurrencyCode)
		if err != nil {
			s.recordArchiveParse(token, "tesseract", "", "", nil, 0, 0, err)
			writeJSON(w, http.StatusBadGateway, errorResponse{Error: "ocr_failed"})
			return
		}
		s.recordArchiveParse(token, "tesseract", ocrResult.Text, ocrResult.Stats.Pass, ocrResult.Items, ocrResult.TaxCents, ocrResult.Confidence, nil)
		ocrText = ocrResult.Text
		ocrConfidence = ocrResult.Confidence

		if err := s.store.UpdateReceiptOCR(receipt.ID, ocrResult.Text); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_store_failed"})
			return
		}
		if err := s.store.UpdateReceiptTax(receipt.ID, ocrResult.TaxCents); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_store_failed"})
			return
		}
		receipt.TaxCents = ocrResult.TaxCents

		items, err = s.store.ReplaceReceiptItems(receipt.ID, ocrResult.Items)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_store_failed"})
			return
		}
		go s.translateAndSave(receipt.ID, items, ocrLang)
	}
	if items == nil {
		items = []store.Item{}
	}

	shareURL := s.shareURL(r, receipt.Token)
	status, refining, pending := s.listMeta(receipt, items)

	writeJSON(w, http.StatusOK, receiptResponse{
		ID: receipt.ID,
		// The uploader is the host, so the token they keep is the host
		// token. The share link and QR code carry the guest token.
		Token:              receipt.HostToken,
		GuestToken:         receipt.Token,
		IsHost:             true,
		LightningAddress:   receipt.LightningAddress,
		ReceiptFilename:    receipt.ReceiptFilename,
		OCRText:            ocrText,
		CreatedAt:          receipt.CreatedAt,
		Items:              items,
		ShareURL:           shareURL,
		ImageURL:           s.imageURL(r, receipt.Token),
		TipPercent:         tipPercentFromBasis(receipt.TipBasisPoints),
		TaxCents:           receipt.TaxCents,
		CurrencyCode:       receipt.CurrencyCode,
		ItemsStatus:        status,
		Refining:           refining,
		PendingTranslation: pending,
		OCRConfidence:      ocrConfidence,
	})
}

func (s *Server) handleReceiptRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/receipt/")
	if path == "" {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	token := parts[0]
	if token == "" {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}

	if len(parts) == 1 && r.Method == http.MethodGet {
		s.handleReceiptGet(w, r, token)
		return
	}
	if len(parts) == 2 && parts[1] == "selection" && r.Method == http.MethodPost {
		s.handleSelectionCreate(w, r, token)
		return
	}
	if len(parts) == 2 && parts[1] == "qr" && r.Method == http.MethodGet {
		s.handleReceiptQR(w, r, token)
		return
	}
	if len(parts) == 2 && parts[1] == "items" && r.Method == http.MethodPut {
		s.handleReceiptItemsUpdate(w, r, token)
		return
	}
	if len(parts) == 2 && parts[1] == "image" && r.Method == http.MethodGet {
		s.handleReceiptImage(w, r, token)
		return
	}
	if len(parts) == 2 && parts[1] == "selections" && r.Method == http.MethodGet {
		s.handleSelectionsGet(w, r, token)
		return
	}
	if len(parts) == 4 && parts[1] == "items" && parts[3] == "split" && r.Method == http.MethodPost {
		s.handleItemSplit(w, r, token, parts[2])
		return
	}
	if len(parts) == 4 && parts[1] == "selections" && parts[3] == "qr" && r.Method == http.MethodGet {
		s.handleSelectionQR(w, r, token, parts[2])
		return
	}

	writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
}

func (s *Server) handleReceiptGet(w http.ResponseWriter, r *http.Request, token string) {
	receiptWithItems, err := s.store.GetReceiptByToken(token)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}

	status, refining, pending := s.listMeta(receiptWithItems.Receipt, receiptWithItems.Items)
	writeJSON(w, http.StatusOK, receiptDetailResponse{
		ID: receiptWithItems.Receipt.ID,
		// Token echoes the token the caller presented, so a guest is never
		// handed the host token.
		Token:              token,
		GuestToken:         receiptWithItems.Receipt.Token,
		IsHost:             isHostToken(receiptWithItems.Receipt, token),
		LightningAddress:   receiptWithItems.Receipt.LightningAddress,
		ReceiptFilename:    receiptWithItems.Receipt.ReceiptFilename,
		OCRText:            receiptWithItems.Receipt.OCRText,
		CreatedAt:          receiptWithItems.Receipt.CreatedAt,
		Items:              receiptWithItems.Items,
		ShareURL:           s.shareURL(r, receiptWithItems.Receipt.Token),
		ImageURL:           s.imageURL(r, receiptWithItems.Receipt.Token),
		TipPercent:         tipPercentFromBasis(receiptWithItems.Receipt.TipBasisPoints),
		TaxCents:           receiptWithItems.Receipt.TaxCents,
		CurrencyCode:       receiptWithItems.Receipt.CurrencyCode,
		ItemsStatus:        status,
		Refining:           refining,
		PendingTranslation: pending,
	})
}

func (s *Server) handleReceiptQR(w http.ResponseWriter, r *http.Request, token string) {
	receiptWithItems, err := s.store.GetReceiptByToken(token)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}
	// The QR code is for guests, so it always carries the guest token, also
	// when the host token was used to ask for it.
	url := s.shareURL(r, receiptWithItems.Receipt.Token)
	png, err := qrcode.Encode(url, qrcode.Medium, 256)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "qr_failed"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

// handleSelectionQR draws one selection's invoice as a QR code for a wallet
// to scan. The selection must belong to the receipt named by token.
func (s *Server) handleSelectionQR(w http.ResponseWriter, r *http.Request, token, selectionIDStr string) {
	selectionID, err := strconv.ParseInt(selectionIDStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}
	receiptWithItems, err := s.store.GetReceiptByToken(token)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}
	selections, err := s.store.ListSelections(receiptWithItems.Receipt.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "selections_fetch_failed"})
		return
	}
	invoice := ""
	for _, sel := range selections {
		if sel.ID == selectionID {
			invoice = sel.Invoice
			break
		}
	}
	if invoice == "" {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}
	// Upper case fits QR's alphanumeric mode, which makes a smaller code.
	png, err := qrcode.Encode(strings.ToUpper("lightning:"+invoice), qrcode.Medium, 320)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "qr_failed"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

func (s *Server) handleReceiptImage(w http.ResponseWriter, r *http.Request, token string) {
	receiptWithItems, err := s.store.GetReceiptByToken(token)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}
	if receiptWithItems.Receipt.ImagePath == "" {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}
	f, err := os.Open(receiptWithItems.Receipt.ImagePath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}
	defer f.Close()
	// The stored file is sent only as the image type its content shows, and
	// never as a document the browser could run on this site's origin.
	head, err := readHead(f)
	kind, ok := sniffImage(head)
	if err != nil || !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}
	info, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}
	w.Header().Set("Content-Type", kind.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", "inline")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func (s *Server) handleSelectionCreate(w http.ResponseWriter, r *http.Request, token string) {
	var payload selectionRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_json"})
		return
	}
	participant := strings.TrimSpace(payload.Participant)
	if participant == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing_participant"})
		return
	}

	receiptWithItems, err := s.store.GetReceiptByToken(token)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}

	items, err := s.store.GetItemsByIDs(receiptWithItems.Receipt.ID, payload.ItemIDs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_items"})
		return
	}
	if len(items) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "no_items_selected"})
		return
	}

	var subtotalCents int64
	quantities := make(map[int64]int64, len(items))
	for _, item := range items {
		units := claimedUnits(payload.Quantities[item.ID], item.Quantity)
		quantities[item.ID] = units
		subtotalCents += splitShareCents(unitsCents(item.AmountCents, units, item.Quantity), item.SplitCount)
	}
	var billCents int64
	for _, item := range receiptWithItems.Items {
		billCents += item.AmountCents
	}
	taxCents := taxShareCents(receiptWithItems.Receipt.TaxCents, subtotalCents, billCents)
	tipCents := applyTipCents(subtotalCents, receiptWithItems.Receipt.TipBasisPoints)
	totalCents := subtotalCents + taxCents + tipCents

	// Only the browser that made a selection may change it. Without this,
	// anyone with the receipt link could replace another person's selection
	// or have their own items counted against that person's payment.
	editSecret, allowed := s.authorizeReplace(receiptWithItems.Receipt.ID, payload.ReplaceSelectionID, payload.EditSecret)
	if !allowed {
		writeJSON(w, http.StatusForbidden, errorResponse{
			Error: "That selection was made on another device, so it cannot be changed from here.",
		})
		return
	}

	// A person who already paid and now changes their selection keeps the
	// paid invoice and gets a second one for the difference.
	invoiceCents := totalCents
	paidRoot, alreadyPaidCents := s.paidSelection(r.Context(), receiptWithItems.Receipt.ID, payload.ReplaceSelectionID)
	if paidRoot != nil {
		invoiceCents = totalCents - alreadyPaidCents
		if invoiceCents <= 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: "You have already paid for this selection. Add items to get a second invoice for the difference; items cannot be removed after paying.",
			})
			return
		}
	}

	amountMSat, btcPrice, err := s.centsToMilliSats(invoiceCents, receiptWithItems.Receipt.CurrencyCode, r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "rate_lookup_failed"})
		return
	}
	lnurl, err := s.lnurl.FetchLNURLPay(r.Context(), receiptWithItems.Receipt.LightningAddress)
	if err != nil {
		log.Printf("invoice: receipt %d: lightning address lookup failed: %v", receiptWithItems.Receipt.ID, err)
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "lnurl_fetch_failed"})
		return
	}
	if amountMSat > lnurl.MaxSendable {
		maxSats := lnurl.MaxSendable / 1000
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: fmt.Sprintf("Amount too large. Max sendable is %d sats — try selecting fewer items.", maxSats),
		})
		return
	}
	if amountMSat < lnurl.MinSendable {
		minSats := (lnurl.MinSendable + 999) / 1000
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: fmt.Sprintf("Amount too small. Min sendable is %d sats.", minSats),
		})
		return
	}
	invoiceComment := fmt.Sprintf("SatSplit: %s", participant)
	invoice, err := s.lnurl.RequestInvoice(r.Context(), lnurl, amountMSat, invoiceComment)
	if err != nil {
		log.Printf("invoice: receipt %d: provider refused %d msat: %v", receiptWithItems.Receipt.ID, amountMSat, err)
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "invoice_failed"})
		return
	}

	selection := store.Selection{
		ReceiptID:   receiptWithItems.Receipt.ID,
		Participant: participant,
		TotalCents:  totalCents,
		Invoice:     invoice.PaymentRequest,
		VerifyURL:   invoice.VerifyURL,
		EditSecret:  editSecret,
		AmountSats:  (amountMSat + 999) / 1000,
		CreatedAt:   time.Now().UTC(),
	}
	selectionID := int64(0)
	if paidRoot != nil {
		selection.TotalCents = invoiceCents
		selection.EditSecret = "" // the paid selection keeps the secret
		selection, err = s.store.AddToPaidSelection(paidRoot.ID, payload.ItemIDs, quantities, selection)
		// The person's selection is still the paid one; it now lists every item.
		selectionID = paidRoot.ID
	} else {
		selection, err = s.store.CreateSelection(selection, payload.ItemIDs, quantities, payload.ReplaceSelectionID)
		selectionID = selection.ID
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "selection_store_failed"})
		return
	}

	writeJSON(w, http.StatusOK, selectionResponse{
		SelectionID:      selectionID,
		Participant:      selection.Participant,
		SubtotalCents:    subtotalCents,
		TaxCents:         taxCents,
		TipPercent:       tipPercentFromBasis(receiptWithItems.Receipt.TipBasisPoints),
		TipCents:         tipCents,
		TotalCents:       totalCents,
		AlreadyPaidCents: alreadyPaidCents,
		InvoiceCents:     invoiceCents,
		EditSecret:       editSecret,
		AmountMSat:       amountMSat,
		CurrencyCode:     receiptWithItems.Receipt.CurrencyCode,
		BTCPrice:         btcPrice,
		Invoice:          selection.Invoice,
		// selection is the stored row for this invoice: the new selection,
		// or the second invoice when adding to a paid one.
		InvoiceSelectionID: selection.ID,
	})
}

func (s *Server) handleSelectionsGet(w http.ResponseWriter, r *http.Request, token string) {
	receiptWithItems, err := s.store.GetReceiptByToken(token)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}
	selections, err := s.store.ListSelections(receiptWithItems.Receipt.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "selections_fetch_failed"})
		return
	}
	if selections == nil {
		selections = []store.SelectionSummary{}
	}
	for i := range selections {
		if hash, err := lightning.PaymentHash(selections[i].Invoice); err == nil {
			selections[i].PaymentHash = hash
		}
	}
	s.refreshPaidStatus(r.Context(), selections)
	writeJSON(w, http.StatusOK, selectionsListResponse{Selections: selections})
}

// authorizeReplace decides whether a request may change the selection it
// names. It returns the secret the resulting selection should carry and
// whether to go ahead. A new selection gets a fresh secret. A selection that
// no longer exists is treated as new, which covers stale browser storage.
// Selections made before secrets existed have none and stay changeable.
func (s *Server) authorizeReplace(receiptID int64, replaceID *int64, presented string) (string, bool) {
	fresh := func() (string, bool) {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return "", false
		}
		return hex.EncodeToString(buf), true
	}
	if replaceID == nil {
		return fresh()
	}
	selections, err := s.store.ListSelections(receiptID)
	if err != nil {
		return "", false
	}
	targetID := *replaceID
	for _, sel := range selections {
		if sel.ID == targetID && sel.ParentID != nil {
			targetID = *sel.ParentID
		}
	}
	for _, sel := range selections {
		if sel.ID != targetID {
			continue
		}
		if sel.EditSecret == "" {
			return fresh()
		}
		if subtle.ConstantTimeCompare([]byte(sel.EditSecret), []byte(presented)) != 1 {
			return "", false
		}
		return sel.EditSecret, true
	}
	return fresh()
}

// paidSelection looks up the selection a person is changing. It returns that
// selection and the amount already paid on it (its own invoice plus any paid
// second invoices) when its invoice is known to be settled, and nil
// otherwise. Settlement is checked with the provider at this moment, so a
// payment made seconds ago counts. With a provider that cannot report
// payments nothing is known to be paid and the old selection is replaced.
func (s *Server) paidSelection(ctx context.Context, receiptID int64, replaceID *int64) (*store.SelectionSummary, int64) {
	if replaceID == nil {
		return nil, 0
	}
	selections, err := s.store.ListSelections(receiptID)
	if err != nil {
		return nil, 0
	}
	rootID := *replaceID
	for _, sel := range selections {
		if sel.ID == rootID && sel.ParentID != nil {
			rootID = *sel.ParentID
		}
	}
	var chain []int
	rootAt := -1
	for i, sel := range selections {
		switch {
		case sel.ID == rootID:
			rootAt = i
			chain = append(chain, i)
		case sel.ParentID != nil && *sel.ParentID == rootID:
			chain = append(chain, i)
		}
	}
	if rootAt < 0 {
		return nil, 0
	}

	checkCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var paidCents int64
	for _, i := range chain {
		sel := &selections[i]
		if !sel.Paid && sel.VerifyURL != "" {
			if result, err := lightning.VerifyPayment(checkCtx, sel.VerifyURL); err != nil {
				log.Printf("verify: selection %d: %v", sel.ID, err)
			} else if result.Settled {
				paidAt := time.Now().UTC()
				if err := s.store.MarkSelectionPaid(sel.ID, result.Preimage, paidAt); err != nil {
					log.Printf("verify: selection %d could not be marked paid: %v", sel.ID, err)
				} else {
					sel.Paid = true
				}
			}
		}
		if sel.Paid {
			paidCents += sel.TotalCents
		}
	}
	if !selections[rootAt].Paid {
		return nil, 0
	}
	return &selections[rootAt], paidCents
}

// verifyInterval is the least time between two settlement checks of the same
// invoice. The host page polls every few seconds; the provider is asked less
// often.
const verifyInterval = 10 * time.Second

// refreshPaidStatus asks the provider about each unpaid invoice that has a
// LUD-21 verify URL and records the ones that were settled. A provider that
// is slow or down leaves the invoice unpaid until a later poll.
func (s *Server) refreshPaidStatus(parent context.Context, selections []store.SelectionSummary) {
	now := time.Now()
	var due []int
	s.verifyMu.Lock()
	if s.verifyLast == nil {
		s.verifyLast = make(map[int64]time.Time)
	}
	for i, sel := range selections {
		if sel.Paid || sel.VerifyURL == "" || now.Sub(s.verifyLast[sel.ID]) < verifyInterval {
			continue
		}
		s.verifyLast[sel.ID] = now
		due = append(due, i)
	}
	s.verifyMu.Unlock()
	if len(due) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, i := range due {
		wg.Add(1)
		go func(sel *store.SelectionSummary) {
			defer wg.Done()
			result, err := lightning.VerifyPayment(ctx, sel.VerifyURL)
			if err != nil {
				log.Printf("verify: selection %d: %v", sel.ID, err)
				return
			}
			if !result.Settled {
				return
			}
			paidAt := time.Now().UTC()
			if err := s.store.MarkSelectionPaid(sel.ID, result.Preimage, paidAt); err != nil {
				log.Printf("verify: selection %d could not be marked paid: %v", sel.ID, err)
				return
			}
			sel.Paid = true
			sel.PaidAt = &paidAt
			sel.Preimage = result.Preimage
			log.Printf("verify: selection %d paid", sel.ID)
		}(&selections[i])
	}
	wg.Wait()
}

func (s *Server) handleReceiptItemsUpdate(w http.ResponseWriter, r *http.Request, token string) {
	var payload itemsUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_json"})
		return
	}
	if payload.TipPercent < 0 || payload.TipPercent > 100 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_tip_percent"})
		return
	}
	if payload.TaxCents < 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_tax"})
		return
	}
	if len(payload.Items) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "no_items"})
		return
	}
	if len(payload.Items) > 200 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "too_many_items"})
		return
	}

	receiptWithItems, err := s.store.GetReceiptByToken(token)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}
	if !isHostToken(receiptWithItems.Receipt, token) {
		writeJSON(w, http.StatusForbidden, errorResponse{
			Error: "Only the person who uploaded this receipt can edit it. Open your host link to make changes.",
		})
		return
	}

	var items []store.Item
	for _, item := range payload.Items {
		label := strings.TrimSpace(item.Label)
		if label == "" {
			continue
		}
		if item.AmountCents < 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_item_amount"})
			return
		}
		split := item.SplitCount
		if split <= 0 {
			split = 1
		}
		quantity := item.Quantity
		if quantity <= 0 {
			quantity = 1
		}
		if quantity > maxItemQuantity {
			quantity = maxItemQuantity
		}
		items = append(items, store.Item{
			Label:       label,
			AmountCents: item.AmountCents,
			SplitCount:  split,
			Quantity:    quantity,
		})
	}
	if len(items) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "no_items"})
		return
	}

	if err := s.store.MarkItemsManual(receiptWithItems.Receipt.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_store_failed"})
		return
	}
	if err := s.store.UpdateReceiptTip(receiptWithItems.Receipt.ID, tipBasisPointsFromPercent(payload.TipPercent)); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_store_failed"})
		return
	}
	if err := s.store.UpdateReceiptTax(receiptWithItems.Receipt.ID, payload.TaxCents); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_store_failed"})
		return
	}
	// Saving must not lose the English names already stored (the vision
	// model's, usually). An item keeps its translation while its label is
	// unchanged; only new or renamed items go to the translation service.
	known := make(map[string]string, len(receiptWithItems.Items))
	for _, old := range receiptWithItems.Items {
		if old.TranslatedLabel != "" {
			known[translationKey(old.Label)] = old.TranslatedLabel
		}
	}
	for i := range items {
		items[i].TranslatedLabel = known[translationKey(items[i].Label)]
	}
	items, err = s.store.ReplaceReceiptItems(receiptWithItems.Receipt.ID, items)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_store_failed"})
		return
	}
	var untranslated []store.Item
	for _, item := range items {
		if item.TranslatedLabel == "" {
			untranslated = append(untranslated, item)
		}
	}
	go s.translateAndSave(receiptWithItems.Receipt.ID, untranslated, "")

	writeJSON(w, http.StatusOK, itemsUpdateResponse{
		Items:      items,
		TipPercent: payload.TipPercent,
		TaxCents:   payload.TaxCents,
	})
}

func (s *Server) handleItemSplit(w http.ResponseWriter, r *http.Request, token string, itemIDStr string) {
	itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
	if err != nil || itemID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_item_id"})
		return
	}

	var payload splitRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_json"})
		return
	}
	delta := payload.Delta
	if delta == 0 {
		delta = 1
	}
	if delta < -1 || delta > 1 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "delta must be 1 or -1"})
		return
	}

	receiptWithItems, err := s.store.GetReceiptByToken(token)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
		return
	}

	if err := s.store.MarkItemsManual(receiptWithItems.Receipt.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "receipt_store_failed"})
		return
	}
	if err := s.store.AdjustItemSplitCount(receiptWithItems.Receipt.ID, itemID, delta); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "item_not_found"})
		return
	}

	// Re-fetch to get updated items
	receiptWithItems, err = s.store.GetReceiptByToken(token)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "fetch_failed"})
		return
	}

	writeJSON(w, http.StatusOK, itemsUpdateResponse{
		Items: receiptWithItems.Items,
	})
}

// translationKey matches an item to its earlier translation by label,
// ignoring case and spacing.
func translationKey(label string) string {
	return strings.ToUpper(strings.Join(strings.Fields(label), " "))
}

func (s *Server) translateAndSave(receiptID int64, items []store.Item, sourceLang string) {
	if s.translator == nil || len(items) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	labels := make([]string, len(items))
	for i, item := range items {
		labels[i] = item.Label
	}
	translated, err := s.translator.TranslateBatch(ctx, labels, sourceLang)
	if err != nil {
		return
	}
	for i := range items {
		items[i].TranslatedLabel = translated[i]
	}
	_ = s.store.UpdateItemTranslations(receiptID, items)
}

func (s *Server) runOCR(ctx context.Context, imagePath, lang, currency string) (ocr.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	res, err := s.ocr.Extract(ctx, imagePath, ocr.Request{Lang: lang, Currency: currency})
	if err != nil {
		log.Printf("OCR: %T failed: %v", s.ocr, err)
		return ocr.Result{}, err
	}
	log.Printf(
		"OCR: %T pass=%s words=%d prices=%d column=%v(%s,tight=%.2f) items=%d confidence=%.2f",
		s.ocr,
		res.Stats.Pass,
		res.Stats.WordCount,
		res.Stats.PriceCandidates,
		res.Stats.ColumnFound,
		res.Stats.PriceFormat,
		res.Stats.ColumnTightness,
		len(res.Items),
		res.Confidence,
	)
	return res, nil
}

// visionReadTimeout bounds one upload's vision pass, retries included.
const visionReadTimeout = 90 * time.Second

// readWithVision reads the receipt with the vision model and stores the
// result. On failure the receipt is left with no items and marked failed, so
// the host can enter them by hand; no other reader is tried.
func (s *Server) readWithVision(parent context.Context, receiptID int64, token, imagePath, currency, ocrHint string) []store.Item {
	log.Printf("vision: starting for receipt %d", receiptID)
	ctx, cancel := context.WithTimeout(parent, visionReadTimeout)
	defer cancel()

	extracted, err := s.vision.ExtractItems(ctx, imagePath, vision.Hint{Currency: currency, OCRText: ocrHint})
	s.recordArchiveVision(token, extracted, err)
	logVisionAttempts(receiptID, extracted)
	if err != nil {
		log.Printf("vision: failed for receipt %d: %v", receiptID, err)
		if statusErr := s.store.UpdateItemsStatus(receiptID, store.ItemsFailed); statusErr != nil {
			log.Printf("vision: status update failed for receipt %d: %v", receiptID, statusErr)
		}
		return nil
	}
	// The archive keeps the lines as the model read them. The receipt gets
	// identical lines folded into one row with a quantity.
	items := mergeDuplicateItems(extracted.Items)
	if len(items) != len(extracted.Items) {
		log.Printf("vision: receipt %d merged %d printed lines into %d rows", receiptID, len(extracted.Items), len(items))
	}
	saved, err := s.store.ReplaceReceiptItems(receiptID, items)
	if err != nil {
		log.Printf("vision: save failed for receipt %d: %v", receiptID, err)
		return nil
	}
	if err := s.store.UpdateReceiptTax(receiptID, extracted.TaxCents); err != nil {
		log.Printf("vision: tax update failed for receipt %d: %v", receiptID, err)
	}
	if err := s.store.UpdateItemsStatus(receiptID, store.ItemsRefined); err != nil {
		log.Printf("vision: status update failed for receipt %d: %v", receiptID, err)
	}
	log.Printf("vision: read %d items for receipt %d", len(saved), receiptID)
	return saved
}

// logVisionAttempts writes one line per model round and a verdict on whether
// the accepted items add up to what the receipt printed.
func logVisionAttempts(receiptID int64, ext vision.Extraction) {
	for i, at := range ext.Attempts {
		if at.Error != "" {
			log.Printf("vision: receipt %d attempt %d model=%q error: %s", receiptID, i+1, at.Model, at.Error)
			continue
		}
		log.Printf(
			"vision: receipt %d attempt %d model=%q items=%d sum=%d subtotal=%d total=%d tax=%d diff=%d balanced=%v tokens=%d+%d",
			receiptID, i+1, at.Model, len(at.Items), at.ItemsSumCents, at.SubtotalCents, at.TotalCents, at.TaxCents,
			at.DiffCents, at.Balanced, at.PromptTokens, at.CompletionTokens,
		)
	}
	if ext.ChargesFromTotal {
		log.Printf("vision: receipt %d charges on top set to printed total less items: %d cents", receiptID, ext.TaxCents)
	}
	if ext.TaxIncluded {
		log.Printf("vision: receipt %d prices already include tax; no tax is added on top", receiptID)
	}
	switch {
	case !ext.Verifiable:
		log.Printf("vision: receipt %d model=%q has no printed subtotal or total to check", receiptID, ext.Model)
	case ext.Balanced:
		log.Printf("vision: receipt %d model=%q balanced after %d attempt(s)", receiptID, ext.Model, len(ext.Attempts))
	default:
		log.Printf("vision: receipt %d model=%q NOT balanced after %d attempt(s), off by %d cents", receiptID, ext.Model, len(ext.Attempts), ext.DiffCents)
	}
}

func (s *Server) listMeta(receipt store.Receipt, items []store.Item) (status string, refining, pending bool) {
	status = receipt.ItemsStatus
	if status == "" {
		status = store.ItemsOCR
	}
	receipt.ItemsStatus = status
	return status, s.refining(receipt), s.pendingTranslation(items)
}

func (s *Server) refining(receipt store.Receipt) bool {
	if s.vision == nil || receipt.ItemsStatus != store.ItemsOCR {
		return false
	}
	if receipt.CreatedAt.IsZero() || time.Since(receipt.CreatedAt) > 3*time.Minute {
		return false
	}
	return true
}

func (s *Server) pendingTranslation(items []store.Item) bool {
	if s.translator == nil || len(items) == 0 {
		return false
	}
	for _, item := range items {
		if strings.TrimSpace(item.TranslatedLabel) == "" {
			return true
		}
	}
	return false
}

func (s *Server) shareURL(r *http.Request, token string) string {
	base := strings.TrimSpace(s.baseURL)
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return strings.TrimRight(base, "/") + "/r/" + token
}

func (s *Server) imageURL(r *http.Request, token string) string {
	base := strings.TrimSpace(s.baseURL)
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return strings.TrimRight(base, "/") + "/api/receipt/" + token + "/image"
}

type cachedPrice struct {
	price     float64
	timestamp time.Time
}

func (s *Server) centsToMilliSats(cents int64, currency string, ctx context.Context) (int64, float64, error) {
	if currency == "" {
		currency = "USD"
	}
	if s.rates == nil {
		return cents * 1000 / s.fiatCentsPerSat, 0, nil
	}
	price, err := s.cachedBTCPrice(ctx, currency)
	if err != nil {
		return 0, 0, err
	}
	if price <= 0 {
		return 0, 0, errors.New("invalid price")
	}
	fiatAmount := float64(cents) / 100.0
	sats := fiatAmount / price * 100_000_000
	// Invoices are for whole sats. Lightning allows a fraction of a sat, but
	// some providers (Blink, for one) and some wallets refuse such amounts.
	wholeSats := int64(sats + 0.5)
	if wholeSats < 1 {
		wholeSats = 1
	}
	return wholeSats * 1000, price, nil
}

func (s *Server) cachedBTCPrice(ctx context.Context, currency string) (float64, error) {
	key := strings.ToUpper(currency)
	s.priceMu.Lock()
	cached, ok := s.priceCache[key]
	s.priceMu.Unlock()
	if ok && time.Since(cached.timestamp) < 60*time.Second {
		return cached.price, nil
	}
	price, err := s.rates.BTCCurrencyPrice(ctx, key)
	if err != nil {
		return 0, err
	}
	s.priceMu.Lock()
	s.priceCache[key] = cachedPrice{price: price, timestamp: time.Now()}
	s.priceMu.Unlock()
	return price, nil
}

// maxItemQuantity bounds the units one receipt line can cover.
const maxItemQuantity = 999

// claimedUnits is how many units of an item a person is charged for. requested
// is what the client sent; zero or missing means one. It never exceeds the
// item's quantity.
func claimedUnits(requested, quantity int64) int64 {
	if quantity <= 0 {
		quantity = 1
	}
	if requested <= 0 {
		return 1
	}
	if requested > quantity {
		return quantity
	}
	return requested
}

// unitsCents is the cost of units out of an item's quantity, rounded to the
// nearest cent. amountCents is the line total for the whole quantity.
func unitsCents(amountCents, units, quantity int64) int64 {
	if quantity <= 1 || units >= quantity {
		return amountCents
	}
	return (amountCents*units + quantity/2) / quantity
}

func splitShareCents(amountCents int64, splitCount int64) int64 {
	if splitCount <= 1 {
		return amountCents
	}
	return (amountCents + splitCount/2) / splitCount
}

// taxShareCents is one diner's portion of a receipt-level tax. selectedCents
// is their food share, billCents is the full food subtotal.
func taxShareCents(taxCents, selectedCents, billCents int64) int64 {
	if taxCents <= 0 || selectedCents <= 0 || billCents <= 0 {
		return 0
	}
	return (taxCents*selectedCents + billCents/2) / billCents
}

func applyTipCents(subtotalCents int64, tipBasisPoints int64) int64 {
	if tipBasisPoints <= 0 {
		return 0
	}
	return subtotalCents * tipBasisPoints / 10000
}

func tipBasisPointsFromPercent(percent float64) int64 {
	if percent <= 0 {
		return 0
	}
	return int64(percent*100 + 0.5)
}

func tipPercentFromBasis(basisPoints int64) float64 {
	if basisPoints <= 0 {
		return 0
	}
	return float64(basisPoints) / 100
}

// errNotAnImage is returned for an upload whose content is not a supported
// image format.
var errNotAnImage = errors.New("upload is not a supported image")

func (s *Server) persistReceiptToDisk(file multipart.File, header *multipart.FileHeader, token string) (string, error) {
	if header.Size > maxReceiptSize {
		return "", errors.New("receipt too large")
	}

	// The type comes from the file's content. The name the browser sent is
	// not used, so a page or script cannot be stored under an image name.
	head, err := readHead(file)
	if err != nil {
		return "", err
	}
	kind, ok := sniffImage(head)
	if !ok {
		return "", errNotAnImage
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	ext := kind.Ext
	if err := os.MkdirAll(s.uploadDir, 0o755); err != nil {
		return "", err
	}

	destPath := filepath.Join(s.uploadDir, token+ext)
	out, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, io.LimitReader(file, maxReceiptSize+1)); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if isHEIC(destPath) {
		converted, err := convertHEICToJPEG(destPath)
		if err != nil {
			// Keep the original so nothing is lost; the reader will report
			// that it could not use the image.
			log.Printf("upload: HEIC conversion failed for %s: %v", token, err)
			return destPath, nil
		}
		log.Printf("upload: converted HEIC to JPEG for %s", token)
		destPath = converted
	}
	return destPath, nil
}

// The guest token is in the share link and its QR code, so it stays short:
// 64 random bits, as it has always been. The host token is the secret that
// allows editing and is never in a QR code, so it is 128 bits.
const (
	guestTokenBytes = 8
	hostTokenBytes  = 16
)

// randomToken returns n random bytes in hex.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// isHostToken reports whether token is the receipt's host token. Receipts
// made before host tokens existed have the same value for both tokens, so
// their one link still acts as host, as it always did.
func isHostToken(receipt store.Receipt, token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(receipt.HostToken), []byte(token)) == 1
}

type receiptResponse struct {
	ID                 int64        `json:"id"`
	Token              string       `json:"token"`
	LightningAddress   string       `json:"lightning_address"`
	ReceiptFilename    string       `json:"receipt_filename"`
	OCRText            string       `json:"ocr_text"`
	CreatedAt          time.Time    `json:"created_at"`
	Items              []store.Item `json:"items"`
	ShareURL           string       `json:"share_url"`
	ImageURL           string       `json:"image_url"`
	TipPercent         float64      `json:"tip_percent"`
	TaxCents           int64        `json:"tax_cents"`
	CurrencyCode       string       `json:"currency_code"`
	ItemsStatus        string       `json:"items_status"`
	Refining           bool         `json:"refining"`
	PendingTranslation bool         `json:"pending_translation"`
	OCRConfidence      float64      `json:"ocr_confidence,omitempty"`
	GuestToken         string       `json:"guest_token"`
	IsHost             bool         `json:"is_host"`
}

type receiptDetailResponse struct {
	ID                 int64        `json:"id"`
	Token              string       `json:"token"`
	LightningAddress   string       `json:"lightning_address"`
	ReceiptFilename    string       `json:"receipt_filename"`
	OCRText            string       `json:"ocr_text"`
	CreatedAt          time.Time    `json:"created_at"`
	Items              []store.Item `json:"items"`
	ShareURL           string       `json:"share_url"`
	ImageURL           string       `json:"image_url"`
	TipPercent         float64      `json:"tip_percent"`
	TaxCents           int64        `json:"tax_cents"`
	CurrencyCode       string       `json:"currency_code"`
	ItemsStatus        string       `json:"items_status"`
	Refining           bool         `json:"refining"`
	PendingTranslation bool         `json:"pending_translation"`
	GuestToken         string       `json:"guest_token"`
	IsHost             bool         `json:"is_host"`
}

type splitRequest struct {
	Delta int64 `json:"delta"`
}

type selectionRequest struct {
	Participant string  `json:"participant"`
	ItemIDs     []int64 `json:"item_ids"`
	// Quantities maps an item ID to the units this person ordered, for items
	// whose quantity is above one. A missing entry means one unit.
	Quantities         map[int64]int64 `json:"quantities,omitempty"`
	ReplaceSelectionID *int64          `json:"replace_selection_id,omitempty"`
	// EditSecret proves the request comes from the browser that created the
	// selection named by ReplaceSelectionID.
	EditSecret string `json:"edit_secret,omitempty"`
}

type selectionResponse struct {
	SelectionID   int64   `json:"selection_id"`
	Participant   string  `json:"participant"`
	SubtotalCents int64   `json:"subtotal_cents"`
	TaxCents      int64   `json:"tax_cents"`
	TipPercent    float64 `json:"tip_percent"`
	TipCents      int64   `json:"tip_cents"`
	TotalCents    int64   `json:"total_cents"`
	AmountMSat    int64   `json:"amount_msat"`
	CurrencyCode  string  `json:"currency_code"`
	BTCPrice      float64 `json:"btc_price"`
	Invoice       string  `json:"invoice"`
	// TotalCents is the whole selection. When the person had already paid,
	// AlreadyPaidCents is what earlier invoices covered and InvoiceCents is
	// what this invoice is for.
	AlreadyPaidCents int64 `json:"already_paid_cents"`
	InvoiceCents     int64 `json:"invoice_cents"`
	// EditSecret must be sent back to change this selection later.
	EditSecret string `json:"edit_secret"`
	// InvoiceSelectionID is the selection row that holds Invoice. It differs
	// from SelectionID on a second invoice, where SelectionID stays the paid
	// selection that lists every item.
	InvoiceSelectionID int64 `json:"invoice_selection_id"`
}

type itemsUpdateRequest struct {
	Items      []store.Item `json:"items"`
	TipPercent float64      `json:"tip_percent"`
	TaxCents   int64        `json:"tax_cents"`
}

type itemsUpdateResponse struct {
	Items      []store.Item `json:"items"`
	TipPercent float64      `json:"tip_percent"`
	TaxCents   int64        `json:"tax_cents"`
}

type selectionsListResponse struct {
	Selections []store.SelectionSummary `json:"selections"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func withJSONHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Never let a browser guess a different type than the one sent.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "not_found"})
			return
		}
		serveStatic(w, r)
	})
}

func serveStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" || path == "/history" || strings.HasPrefix(path, "/r/") || strings.HasPrefix(path, "/h/") {
		serveStaticAsset(w, "index.html", "text/html; charset=utf-8")
		return
	}
	switch path {
	case "/index.html":
		serveStaticAsset(w, "index.html", "text/html; charset=utf-8")
	case "/styles.css":
		serveStaticAsset(w, "styles.css", "text/css; charset=utf-8")
	case "/app.js":
		serveStaticAsset(w, "app.js", "text/javascript; charset=utf-8")
	case "/favicon.ico":
		w.WriteHeader(http.StatusNoContent)
	default:
		if name, ok := strings.CutPrefix(path, "/fonts/"); ok && fontNamePattern.MatchString(name) {
			serveFont(w, name)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}
}

// fontNamePattern admits plain file names only, so a request cannot reach
// outside static/fonts/.
var fontNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+\.woff2$`)

func serveFont(w http.ResponseWriter, name string) {
	data, err := readStaticFile("fonts/" + name)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "font/woff2")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func serveStaticAsset(w http.ResponseWriter, name, contentType string) {
	data, err := readStaticFile(name)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
