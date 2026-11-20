package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"satsplit/internal/ocr"
	"satsplit/internal/store"
)

// Config points at an OpenAI-compatible chat endpoint. That is llama.cpp's
// local server, or a hosted vision API when BaseURL and APIKey are set.
// Local Tesseract remains the fallback when this call fails.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	// MaxAttempts bounds the rounds spent getting the items to add up.
	// Zero means the default.
	MaxAttempts int
}

type Client struct {
	baseURL     string
	apiKey      string
	model       string
	maxAttempts int
	httpClient  *http.Client

	mu     sync.Mutex
	quirks quirks
}

func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 180 * time.Second
	}
	model := cfg.Model
	if cfg.APIKey != "" && model == "" {
		model = "gpt-4o-mini"
	}
	attempts := cfg.MaxAttempts
	if attempts <= 0 {
		attempts = maxAttempts
	}
	return &Client{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		model:       model,
		maxAttempts: attempts,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// NewLlamaCppClient keeps the previous constructor for a local server with no API key.
func NewLlamaCppClient(baseURL string) *Client {
	return NewClient(Config{BaseURL: baseURL})
}

func promptFor(hint Hint) string {
	currency := strings.TrimSpace(hint.Currency)
	if currency == "" {
		currency = "unknown (assume two decimal places)"
	}
	draft := strings.TrimSpace(hint.OCRText)
	if len(draft) > 3500 {
		draft = draft[:3500]
	}
	draftSection := ""
	if draft != "" {
		draftSection = "\nOCR draft, which may contain errors (prefer the image when they disagree):\n" + draft
	}
	return `You extract line items from a restaurant or retail receipt photo.

Return a JSON object and nothing else, using this shape:
{"items":[{"label":"string","label_en":"string","quantity":1,"amount":"0.00"}],"tax":"0.00","subtotal":"0.00","total":"0.00"}

Rules:
- label is the item name as printed
- label_en is an English translation, or the same as label when it is already English
- when the printer cut a name short (for example "PECHUGA A LA PLANC"), write the full name in label if it is obvious, and translate the full name in label_en
- amount is the line total as printed, with a dot as the decimal separator and no currency symbol. 12.50 means twelve and a half, not 1250 cents
- one object per printed line. Do not split a quantity into multiple objects
- an item name can wrap onto the next printed line. Join the wrapped lines into one label, in reading order
- quantity is the number of units printed for that line, as a whole number. Use 1 when no quantity is printed. amount stays the line total for all units
- if both a unit price and a line total are printed, use the line total
- include only purchasable items in items
- tax is every printed charge added on top of the items, combined into one amount: sales tax, VAT, IVA, ITBIS, and any service charge that is part of the grand total (servicio, 10% Ley, propina legal, service charge). Use a decimal string. Use "0.00" when the receipt has none. subtotal plus tax should equal the printed total
- do not put those charges in items
- do not put subtotal, tip, gratuity, change, payment method, or the grand total in items
- subtotal is the printed subtotal before tax and service charges. total is the printed grand total. Use "" for one that is not printed
- the item amounts must add up to the printed subtotal. Add them up and check before answering

Currency: ` + currency + draftSection
}

type chatRequest struct {
	Model               string          `json:"model,omitempty"`
	Messages            []chatMessage   `json:"messages"`
	ResponseFormat      *responseFormat `json:"response_format,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Stream              bool            `json:"stream"`
}

// chatMessage content is a []contentPart for user turns that carry an image
// and a plain string otherwise.
type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

type chatChoice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type extractedItem struct {
	Label       string          `json:"label"`
	Name        string          `json:"name"`
	LabelEN     string          `json:"label_en"`
	Amount      json.RawMessage `json:"amount"`
	Price       json.RawMessage `json:"price"`
	AmountCents int64           `json:"amount_cents"`
	Quantity    json.RawMessage `json:"quantity"`
}

// maxAttempts is the default bound on how often the model is asked to fix a receipt whose
// items do not add up to the printed subtotal.
const maxAttempts = 3

// reasoningMaxTokens is the output budget for models that take
// max_completion_tokens, where hidden reasoning counts against the limit.
const reasoningMaxTokens = 8192

func (c *Client) ExtractItems(ctx context.Context, imagePath string, hint Hint) (Extraction, error) {
	path := imagePath
	cleanup := func() {}
	prepared, prepCleanup, err := ocr.PrepareImage(imagePath, ocr.PrepOptions{
		MaxLongEdge: 1600,
		JPEGQuality: 82,
	})
	if err == nil {
		path = prepared
		cleanup = prepCleanup
	}
	defer cleanup()

	imgData, err := os.ReadFile(path)
	if err != nil {
		return Extraction{Model: c.model}, fmt.Errorf("vision: read image: %w", err)
	}

	mime := mimeType(path)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(imgData))
	messages := []chatMessage{
		{
			Role: "user",
			Content: []contentPart{
				{Type: "text", Text: promptFor(hint)},
				{Type: "image_url", ImageURL: &imageURL{URL: dataURL}},
			},
		},
	}

	var (
		attempts []Attempt
		best     Extraction
		haveBest bool
		lastErr  error
	)
	for n := 0; n < c.maxAttempts; n++ {
		content, usage, err := c.complete(ctx, messages)
		if err != nil {
			lastErr = err
			attempts = append(attempts, Attempt{Model: c.model, Error: err.Error()})
			break
		}
		at := Attempt{
			Model:            c.model,
			Raw:              content,
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
		}
		ext, err := parseItems(content, hint.Currency)
		if err != nil {
			lastErr = err
			at.Error = err.Error()
			attempts = append(attempts, at)
			messages = append(messages,
				chatMessage{Role: "assistant", Content: content},
				chatMessage{Role: "user", Content: "That reply could not be used: " + err.Error() + ". Return the full JSON object in the requested shape and nothing else."},
			)
			continue
		}
		target, diff, verifiable := ext.balance()
		if verifiable && diff == 0 && ext.TaxCents > 0 && ext.TotalCents > 0 && abs64(sumItems(ext.Items)-ext.TotalCents) <= 1 {
			// The items already add up to the grand total, so the printed
			// tax is inside the prices. Adding it again would overcharge.
			ext.TaxIncluded = true
			ext.TaxCents = 0
		}
		if sum := sumItems(ext.Items); verifiable && diff == 0 && !ext.TaxIncluded {
			// The printed total is what the table owes. When it is more than
			// the items plus the tax that was read, a charge was missed (most
			// often a service charge), so the whole gap is split instead. A
			// gap above half the items is taken as a misread total.
			if gap := ext.TotalCents - sum; gap > ext.TaxCents+1 && gap <= sum/2 {
				ext.ChargesFromTotal = true
				ext.TaxCents = gap
			}
		}
		ext.Verifiable = verifiable
		ext.Balanced = verifiable && diff == 0
		ext.DiffCents = diff

		at.Items = ext.Items
		at.TaxCents = ext.TaxCents
		at.SubtotalCents = ext.SubtotalCents
		at.TotalCents = ext.TotalCents
		at.ItemsSumCents = sumItems(ext.Items)
		at.DiffCents = diff
		at.Balanced = ext.Balanced
		attempts = append(attempts, at)

		if !haveBest || abs64(diff) <= abs64(best.DiffCents) {
			best = ext
			haveBest = true
		}
		if !verifiable || diff == 0 {
			break
		}
		messages = append(messages,
			chatMessage{Role: "assistant", Content: content},
			chatMessage{Role: "user", Content: mismatchFeedback(at.ItemsSumCents, target, ext.SubtotalCents > 0)},
		)
	}
	if !haveBest {
		if lastErr == nil {
			lastErr = fmt.Errorf("vision: no usable reply")
		}
		return Extraction{Model: c.model, Attempts: attempts}, lastErr
	}
	best.Model = c.model
	best.Attempts = attempts
	return best, nil
}

// balance compares the item sum with what the receipt printed. It reports the
// figure the items should match, the difference from it, and whether the
// receipt printed anything to check against. Prices can be listed before or
// after tax, so a match with the subtotal, the total less tax, or the total
// all count.
func (e Extraction) balance() (target, diff int64, verifiable bool) {
	sum := sumItems(e.Items)
	var candidates []int64
	if e.SubtotalCents > 0 {
		candidates = append(candidates, e.SubtotalCents)
	}
	if e.TotalCents > 0 {
		if e.TaxCents > 0 && e.TotalCents > e.TaxCents {
			candidates = append(candidates, e.TotalCents-e.TaxCents)
		}
		candidates = append(candidates, e.TotalCents)
	}
	if len(candidates) == 0 {
		return 0, 0, false
	}
	for _, c := range candidates {
		if abs64(sum-c) <= 1 {
			return c, 0, true
		}
	}
	return candidates[0], sum - candidates[0], true
}

func mismatchFeedback(sum, target int64, isSubtotal bool) string {
	name := "printed total less tax"
	if isSubtotal {
		name = "printed subtotal"
	}
	return fmt.Sprintf(
		"The item amounts you returned add up to %s, but the %s is %s (a difference of %s). "+
			"Read the receipt image again line by line. Check that each amount is paired with the label printed on its own line, "+
			"that no item is missing or listed twice, and that no line that is not an item was included. "+
			"If you misread the subtotal, total, or tax instead, correct those. "+
			"Return the full corrected JSON object in the same shape and nothing else.",
		formatCents(sum), name, formatCents(target), formatCents(abs64(sum-target)),
	)
}

func sumItems(items []store.Item) int64 {
	var sum int64
	for _, it := range items {
		sum += it.AmountCents
	}
	return sum
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func formatCents(c int64) string {
	sign := ""
	if c < 0 {
		sign = "-"
		c = -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

// quirks are request-shape differences between servers and models, learned
// from 400 responses and remembered for later calls.
type quirks struct {
	noResponseFormat    bool
	noTemperature       bool
	maxCompletionTokens bool
}

func (c *Client) newRequest(messages []chatMessage, q quirks) chatRequest {
	req := chatRequest{
		Model:    c.model,
		Messages: messages,
		Stream:   false,
	}
	if !q.noResponseFormat {
		req.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	if !q.noTemperature {
		zero := 0.0
		req.Temperature = &zero
	}
	if q.maxCompletionTokens {
		req.MaxCompletionTokens = reasoningMaxTokens
	} else {
		req.MaxTokens = 2048
	}
	return req
}

// learnQuirk reads a 400 response and adjusts the request shape. It reports
// false when nothing is left to change. Newer hosted models reject max_tokens
// and a fixed temperature; some llama.cpp builds reject response_format.
func learnQuirk(q *quirks, errText string) bool {
	low := strings.ToLower(errText)
	switch {
	case !q.maxCompletionTokens && strings.Contains(low, "max_tokens"):
		q.maxCompletionTokens = true
	case !q.noTemperature && strings.Contains(low, "temperature"):
		q.noTemperature = true
	case !q.noResponseFormat:
		q.noResponseFormat = true
	default:
		return false
	}
	return true
}

func (c *Client) complete(ctx context.Context, messages []chatMessage) (string, chatUsage, error) {
	c.mu.Lock()
	q := c.quirks
	c.mu.Unlock()

	for {
		content, usage, status, err := c.post(ctx, c.newRequest(messages, q))
		if err == nil {
			c.mu.Lock()
			c.quirks = q
			c.mu.Unlock()
			return content, usage, nil
		}
		if status != http.StatusBadRequest || !learnQuirk(&q, err.Error()) {
			return "", chatUsage{}, err
		}
	}
}

func (c *Client) post(ctx context.Context, reqBody chatRequest) (string, chatUsage, int, error) {
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", chatUsage{}, 0, fmt.Errorf("vision: marshal request: %w", err)
	}
	url := c.baseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", chatUsage{}, 0, fmt.Errorf("vision: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", chatUsage{}, 0, fmt.Errorf("vision: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4000))
		return "", chatUsage{}, resp.StatusCode, fmt.Errorf("vision: status %d: %s", resp.StatusCode, truncate(string(respBody), 400))
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", chatUsage{}, resp.StatusCode, fmt.Errorf("vision: decode response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", chatUsage{}, resp.StatusCode, fmt.Errorf("vision: empty response")
	}
	return chatResp.Choices[0].Message.Content, chatResp.Usage, resp.StatusCode, nil
}

func parseItems(response, currency string) (Extraction, error) {
	text := extractJSON(response)

	var fieldTax, subtotal, total int64
	var items []extractedItem
	if err := json.Unmarshal([]byte(text), &items); err != nil {
		var wrapped struct {
			Items    []extractedItem `json:"items"`
			Tax      json.RawMessage `json:"tax"`
			Subtotal json.RawMessage `json:"subtotal"`
			Total    json.RawMessage `json:"total"`
		}
		if err2 := json.Unmarshal([]byte(text), &wrapped); err2 != nil {
			return Extraction{}, fmt.Errorf("vision: parse items: %w (raw: %s)", err, truncate(text, 200))
		}
		items = wrapped.Items
		if cents, ok := amountFromRaw(wrapped.Tax, currency); ok {
			fieldTax = cents
		}
		if cents, ok := amountFromRaw(wrapped.Subtotal, currency); ok {
			subtotal = cents
		}
		if cents, ok := amountFromRaw(wrapped.Total, currency); ok {
			total = cents
		}
	}

	var result []store.Item
	var lineTax int64
	for _, ei := range items {
		label := strings.TrimSpace(ei.Label)
		if label == "" {
			label = strings.TrimSpace(ei.Name)
		}
		if label == "" {
			continue
		}
		cents, ok := amountFromItem(ei, currency)
		if !ok || cents <= 0 {
			continue
		}
		if ocr.IsAllocatableCharge(label) {
			lineTax += cents
			continue
		}
		result = append(result, store.Item{
			Label:           label,
			TranslatedLabel: strings.TrimSpace(ei.LabelEN),
			AmountCents:     cents,
			SplitCount:      1,
			Quantity:        quantityFromRaw(ei.Quantity),
		})
	}
	if len(result) == 0 {
		return Extraction{}, ErrNoItems
	}
	tax := lineTax
	if fieldTax > 0 {
		tax = fieldTax
	}
	return Extraction{Items: result, TaxCents: tax, SubtotalCents: subtotal, TotalCents: total}, nil
}

// quantityFromRaw reads a unit count the model may send as a number or a
// string such as "2" or "2.00". Anything unusable counts as one unit.
func quantityFromRaw(raw json.RawMessage) int64 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 1
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 1
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "x")), 64)
		if err != nil {
			return 1
		}
		f = parsed
	}
	if math.IsNaN(f) || f < 1 || f > 99 || f != math.Trunc(f) {
		return 1
	}
	return int64(f)
}

func amountFromRaw(raw json.RawMessage, currency string) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return ocr.ParsePrintedAmount(s, currency)
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil && f > 0 && !math.IsNaN(f) && !math.IsInf(f, 0) {
		return int64(math.Round(f * 100)), true
	}
	return 0, false
}

func amountFromItem(ei extractedItem, currency string) (int64, bool) {
	raw := bytes.TrimSpace(ei.Amount)
	if len(raw) == 0 || string(raw) == "null" {
		raw = bytes.TrimSpace(ei.Price)
	}
	if len(raw) > 0 && string(raw) != "null" {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return ocr.ParsePrintedAmount(s, currency)
		}
		var f float64
		if err := json.Unmarshal(raw, &f); err == nil && f > 0 && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return int64(math.Round(f * 100)), true
		}
	}
	if ei.AmountCents > 0 {
		return ei.AmountCents, true
	}
	return 0, false
}

func extractJSON(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	if start := strings.IndexAny(text, "{["); start > 0 {
		text = text[start:]
	}
	return strings.TrimSpace(text)
}

func mimeType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
