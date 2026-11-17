package rates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type PriceService interface {
	BTCCurrencyPrice(ctx context.Context, currency string) (float64, error)
}

type Coinbase struct {
	client *http.Client
}

func NewCoinbase() *Coinbase {
	return &Coinbase{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Coinbase) BTCCurrencyPrice(ctx context.Context, currency string) (float64, error) {
	if currency == "" {
		return 0, errors.New("currency required")
	}
	url := fmt.Sprintf("https://api.coinbase.com/v2/prices/BTC-%s/spot", currency)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var payload struct {
		Data struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	if payload.Data.Amount == "" {
		return 0, errors.New("price missing")
	}
	price, err := strconv.ParseFloat(payload.Data.Amount, 64)
	if err != nil {
		return 0, err
	}
	if price <= 0 {
		return 0, errors.New("invalid price")
	}
	return price, nil
}
