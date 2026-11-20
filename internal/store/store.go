package store

import (
	"errors"
	"time"
)

const (
	// ItemsOCR is the fast local parse. A configured vision pass may still replace it.
	ItemsOCR = "ocr"
	// ItemsRefined means a vision pass replaced the local parse.
	ItemsRefined = "refined"
	// ItemsFailed means vision was tried and the local parse was kept.
	ItemsFailed = "failed"
	// ItemsManual means a person edited the list. Automatic replacement must stop.
	ItemsManual = "manual"

	// ReadNoItems means the photo produced no purchasable lines.
	ReadNoItems = "no_items"
	// ReadTimeout means the reader did not finish in time.
	ReadTimeout = "timeout"
	// ReadUnavailable means the reader could not be reached.
	ReadUnavailable = "unavailable"
)

// ErrUploadExists means this client upload id already has a receipt.
var ErrUploadExists = errors.New("upload already stored")

type Receipt struct {
	ID int64 `json:"id"`
	// Token is the guest token, the one in the share link. Anyone who has it
	// can view the receipt and claim items.
	Token string `json:"token"`
	// HostToken is known only to the person who uploaded the receipt. It is
	// required to edit the items, tax and tip. Receipts made before host
	// tokens existed have HostToken equal to Token, so their old links keep
	// working exactly as before. It is never serialised.
	HostToken        string    `json:"-"`
	LightningAddress string    `json:"lightning_address"`
	ReceiptFilename  string    `json:"receipt_filename"`
	ImagePath        string    `json:"image_path"`
	TipBasisPoints   int64     `json:"tip_basis_points"`
	TaxCents         int64     `json:"tax_cents"`
	CurrencyCode     string    `json:"currency_code"`
	OCRText          string    `json:"ocr_text"`
	ItemsStatus      string    `json:"items_status"`
	CreatedAt        time.Time `json:"created_at"`
	// UploadID is the browser's id for one upload attempt. A retry with the
	// same id returns this receipt instead of creating another.
	UploadID string `json:"-"`
	// ReadIssue is why a vision pass did not produce a usable list.
	// Empty means the pass saved items, which may still disagree with the
	// printed total (see BalanceChecked).
	ReadIssue string `json:"-"`
	// BalanceChecked is true when the receipt printed a figure to compare.
	BalanceChecked bool `json:"-"`
	// BalanceDiffCents is the item sum minus that figure.
	BalanceDiffCents int64 `json:"-"`
}

type Item struct {
	ID              int64  `json:"id"`
	ReceiptID       int64  `json:"receipt_id"`
	Label           string `json:"label"`
	TranslatedLabel string `json:"translated_label"`
	// AmountCents is the line total for all Quantity units.
	AmountCents int64 `json:"amount_cents"`
	// SplitCount is how many people share the item.
	SplitCount int64 `json:"split_count"`
	// Quantity is how many units the line covers. Each person claims the
	// number of units they ordered. It is separate from SplitCount.
	Quantity int64 `json:"quantity"`
}

type Selection struct {
	ID          int64  `json:"id"`
	ReceiptID   int64  `json:"receipt_id"`
	Participant string `json:"participant"`
	TotalCents  int64  `json:"total_cents"`
	Invoice     string `json:"invoice"`
	// ParentID is set on a second invoice: it covers what was added to the
	// paid selection ParentID, which keeps the full item list.
	ParentID *int64 `json:"parent_id,omitempty"`
	// EditSecret is known only to the browser that created the selection.
	// It must be presented to change the selection later.
	EditSecret string `json:"-"`
	// VerifyURL is the provider's LUD-21 URL for this invoice, or empty.
	VerifyURL  string    `json:"-"`
	AmountSats int64     `json:"amount_sats"`
	CreatedAt  time.Time `json:"created_at"`
}

type SelectionItemRef struct {
	ID       int64  `json:"id"`
	Label    string `json:"label"`
	Quantity int64  `json:"quantity"`
}

type SelectionSummary struct {
	ID          int64              `json:"id"`
	Participant string             `json:"participant"`
	TotalCents  int64              `json:"total_cents"`
	Invoice     string             `json:"invoice"`
	Items       []SelectionItemRef `json:"items"`
	CreatedAt   time.Time          `json:"created_at"`

	// ParentID is set on a second invoice for items added after paying. Such
	// a row has no items; they are listed on the selection it adds to.
	ParentID   *int64 `json:"parent_id,omitempty"`
	AmountSats int64  `json:"amount_sats"`
	// PaymentHash identifies the invoice in a wallet's payment list.
	PaymentHash string `json:"payment_hash,omitempty"`
	// Verifiable is true when the provider can report settlement (LUD-21).
	// When false, Paid says nothing and the host matches by payment hash.
	Verifiable bool       `json:"verifiable"`
	Paid       bool       `json:"paid"`
	PaidAt     *time.Time `json:"paid_at,omitempty"`
	Preimage   string     `json:"preimage,omitempty"`
	VerifyURL  string     `json:"-"`
	// EditSecret is never sent to clients in a listing.
	EditSecret string `json:"-"`
}

type ReceiptWithItems struct {
	Receipt Receipt
	Items   []Item
}

type Store interface {
	CreateReceipt(receipt Receipt) (Receipt, error)
	// GetReceiptByUploadID finds the receipt stored for a client upload id.
	GetReceiptByUploadID(uploadID string) (ReceiptWithItems, error)
	// SetReadState records the outcome of a vision pass. It does nothing once
	// a person has edited the list or anyone has claimed an item.
	SetReadState(receiptID int64, issue string, balanceChecked bool, balanceDiffCents int64) error
	UpdateReceiptOCR(receiptID int64, ocrText string) error
	UpdateReceiptTip(receiptID int64, tipBasisPoints int64) error
	UpdateReceiptTax(receiptID int64, taxCents int64) error
	ReplaceReceiptItems(receiptID int64, items []Item) ([]Item, error)
	// ReplaceAutoItems replaces items only while the receipt is still the
	// untouched OCR draft and nobody has created a selection against it.
	ReplaceAutoItems(receiptID int64, items []Item, status string) (bool, []Item, error)
	MarkItemsManual(receiptID int64) error
	UpdateItemsStatus(receiptID int64, status string) error
	UpdateItemTranslations(receiptID int64, items []Item) error
	// GetReceiptByToken finds a receipt by its host token or its guest
	// token. Compare the token with Receipt.HostToken to tell which it was.
	GetReceiptByToken(token string) (ReceiptWithItems, error)
	GetItemsByIDs(receiptID int64, itemIDs []int64) ([]Item, error)
	AdjustItemSplitCount(receiptID int64, itemID int64, delta int64) error
	// CreateSelection records the items a participant claimed. quantities maps
	// an item ID to the units claimed; a missing entry means one unit.
	CreateSelection(selection Selection, itemIDs []int64, quantities map[int64]int64, replaceID *int64) (Selection, error)
	ListSelections(receiptID int64) ([]SelectionSummary, error)
	// AddToPaidSelection is used when a person changes a selection they have
	// already paid for. The paid selection rootID stays and takes the new item
	// list; addOn is stored as a second invoice for the difference. Unpaid
	// earlier second invoices of rootID are dropped.
	AddToPaidSelection(rootID int64, itemIDs []int64, quantities map[int64]int64, addOn Selection) (Selection, error)
	// MarkSelectionPaid records that the selection's invoice was settled.
	MarkSelectionPaid(selectionID int64, preimage string, paidAt time.Time) error
}
