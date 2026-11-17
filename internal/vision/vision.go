package vision

import (
	"context"

	"satsplit/internal/store"
)

// Hint is local context for a vision pass. OCRText is a draft transcription;
// the image still wins when they disagree.
type Hint struct {
	OCRText  string
	Currency string
}

// Extraction is the accepted result of a vision pass. When the receipt prints
// a subtotal or total, the model is asked again until the item amounts add up
// to it or the attempt limit is reached; Attempts records every round.
type Extraction struct {
	Items         []store.Item
	TaxCents      int64
	SubtotalCents int64
	TotalCents    int64

	// Model is the model name sent to the API. Empty means the server default.
	Model string
	// Verifiable is false when the receipt printed nothing to check against.
	Verifiable bool
	// Balanced is true when the items add up to the printed subtotal or total.
	Balanced bool
	// DiffCents is the item sum minus the printed figure. Zero when balanced.
	DiffCents int64
	// TaxIncluded is true when the item prices already contain the tax, so
	// the items add up to the grand total. TaxCents is zero in that case
	// because the app adds TaxCents on top of the items.
	TaxIncluded bool
	// ChargesFromTotal is true when TaxCents was set to the printed total
	// less the items because the charges read from the receipt did not
	// cover that gap (a service charge was missed, for example).
	ChargesFromTotal bool
	Attempts         []Attempt
}

// Attempt is one request and reply in a vision pass.
type Attempt struct {
	Model            string       `json:"model,omitempty"`
	Raw              string       `json:"raw,omitempty"`
	Items            []store.Item `json:"items,omitempty"`
	TaxCents         int64        `json:"tax_cents"`
	SubtotalCents    int64        `json:"subtotal_cents"`
	TotalCents       int64        `json:"total_cents"`
	ItemsSumCents    int64        `json:"items_sum_cents"`
	DiffCents        int64        `json:"diff_cents"`
	Balanced         bool         `json:"balanced"`
	PromptTokens     int          `json:"prompt_tokens,omitempty"`
	CompletionTokens int          `json:"completion_tokens,omitempty"`
	Error            string       `json:"error,omitempty"`
}

type Service interface {
	ExtractItems(ctx context.Context, imagePath string, hint Hint) (Extraction, error)
}
