package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Service interface {
	TranslateBatch(ctx context.Context, texts []string, sourceLang string) ([]string, error)
}

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	if baseURL == "" {
		return nil
	}
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type translateRequest struct {
	Texts      []string `json:"texts"`
	SourceLang string   `json:"source_lang"`
}

type translateResponse struct {
	Translations []string `json:"translations"`
}

func (c *Client) TranslateBatch(ctx context.Context, texts []string, sourceLang string) ([]string, error) {
	if len(texts) == 0 {
		return texts, nil
	}
	body, err := json.Marshal(translateRequest{
		Texts:      texts,
		SourceLang: sourceLang,
	})
	if err != nil {
		return texts, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/translate", bytes.NewReader(body))
	if err != nil {
		return texts, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return texts, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return texts, fmt.Errorf("translate: status %d", resp.StatusCode)
	}

	var result translateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return texts, err
	}
	if len(result.Translations) != len(texts) {
		return texts, fmt.Errorf("translate: got %d results for %d texts", len(result.Translations), len(texts))
	}
	return result.Translations, nil
}
