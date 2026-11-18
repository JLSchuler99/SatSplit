package lightning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// maxLNURLResponse bounds how much of a Lightning address provider's reply
// is read. The provider is whatever host the uploader typed, so an endless or
// huge reply must not be read into memory. Real replies are a few kilobytes.
const maxLNURLResponse = 256 << 10

var invoicePattern = regexp.MustCompile(`(?i)^ln[a-z0-9]+$`)

var lightningAddressPattern = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

type LNURLPayResponse struct {
	Callback       string `json:"callback"`
	MinSendable    int64  `json:"minSendable"`
	MaxSendable    int64  `json:"maxSendable"`
	CommentAllowed int64  `json:"commentAllowed"`
	Metadata       string `json:"metadata"`
	Tag            string `json:"tag"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
}

type InvoiceResponse struct {
	PR             string `json:"pr"`
	PaymentRequest string `json:"payment_request"`
	Verify         string `json:"verify"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
}

// Invoice is what an LNURL-pay callback returned. VerifyURL is set when the
// provider supports LUD-21; it can be polled to learn whether the invoice
// was paid.
type Invoice struct {
	PaymentRequest string
	VerifyURL      string
}

// VerifyResult is a LUD-21 answer.
type VerifyResult struct {
	Settled  bool
	Preimage string
}

type verifyResponse struct {
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	Settled  bool   `json:"settled"`
	Preimage string `json:"preimage"`
}

// cleanVerifyURL accepts only an https URL. The value comes from a remote
// server and is fetched later, so anything else is dropped.
func cleanVerifyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	return parsed.String()
}

// newPublicClient returns a client for requests whose destination a remote
// server chose: a Lightning address host, an LNURL callback, or a LUD-21
// verify URL. It refuses to connect to addresses that are not public, checked
// on the address actually dialled after DNS resolution, so a provider cannot
// point the server at something inside its own network. It does not follow
// redirects, which could lead anywhere.
func newPublicClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout: 4 * time.Second,
				Control: func(network, address string, _ syscall.RawConn) error {
					host, _, err := net.SplitHostPort(address)
					if err != nil {
						return err
					}
					if !allowAddress(net.ParseIP(host)) {
						return fmt.Errorf("destination %s is not public", host)
					}
					return nil
				},
			}).DialContext,
			TLSHandshakeTimeout: 4 * time.Second,
			MaxIdleConns:        4,
			IdleConnTimeout:     30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// verifyClient fetches LUD-21 verify URLs.
var verifyClient = newPublicClient(5 * time.Second)

// allowAddress decides which dialled addresses a request whose destination a
// remote server chose may reach. Tests replace it to reach a stand-in
// provider on this machine.
var allowAddress = isPublicIP

var carrierNAT = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// isPublicIP reports whether ip is an ordinary internet address. The check
// runs on the address actually dialled, after DNS resolution.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	return !carrierNAT.Contains(ip)
}

// VerifyPayment asks a LUD-21 verify URL whether its invoice was settled.
func VerifyPayment(ctx context.Context, verifyURL string) (VerifyResult, error) {
	verifyURL = cleanVerifyURL(verifyURL)
	if verifyURL == "" {
		return VerifyResult{}, errors.New("invalid verify url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, verifyURL, nil)
	if err != nil {
		return VerifyResult{}, err
	}
	resp, err := verifyClient.Do(req)
	if err != nil {
		return VerifyResult{}, err
	}
	defer resp.Body.Close()

	var payload verifyResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxLNURLResponse)).Decode(&payload); err != nil {
		return VerifyResult{}, err
	}
	if payload.Status == "ERROR" {
		return VerifyResult{}, errors.New(payload.Reason)
	}
	return VerifyResult{Settled: payload.Settled, Preimage: strings.TrimSpace(payload.Preimage)}, nil
}

// providerClient makes the LNURL-pay requests: the lookup of a Lightning
// address, whose host the uploader typed, and the invoice callback, whose URL
// that host returned.
var providerClient = newPublicClient(10 * time.Second)

func ValidateAddressSyntax(address string) bool {
	return lightningAddressPattern.MatchString(address)
}

func FetchLNURLPay(ctx context.Context, address string) (LNURLPayResponse, error) {
	user, host, ok := splitAddress(address)
	if !ok {
		return LNURLPayResponse{}, errors.New("invalid lightning address")
	}

	url := fmt.Sprintf("https://%s/.well-known/lnurlp/%s", host, user)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return LNURLPayResponse{}, err
	}

	resp, err := providerClient.Do(req)
	if err != nil {
		return LNURLPayResponse{}, err
	}
	defer resp.Body.Close()

	var payload LNURLPayResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxLNURLResponse)).Decode(&payload); err != nil {
		return LNURLPayResponse{}, err
	}
	if payload.Status == "ERROR" {
		return LNURLPayResponse{}, errors.New(payload.Reason)
	}
	if payload.Callback == "" {
		return LNURLPayResponse{}, errors.New("lnurl callback missing")
	}
	return payload, nil
}

func RequestInvoice(ctx context.Context, lnurl LNURLPayResponse, amountMSat int64, comment string) (Invoice, error) {
	if amountMSat < lnurl.MinSendable || amountMSat > lnurl.MaxSendable {
		return Invoice{}, fmt.Errorf("amount out of bounds")
	}

	parsed, err := url.Parse(lnurl.Callback)
	if err != nil {
		return Invoice{}, err
	}
	// The callback comes from the provider's reply. LNURL (LUD-01) expects
	// https; its exception for Tor onion services does not apply, since this
	// server has no route to them.
	if parsed.Scheme != "https" || parsed.Host == "" {
		return Invoice{}, errors.New("lnurl callback is not an https url")
	}
	q := parsed.Query()
	q.Set("amount", fmt.Sprintf("%d", amountMSat))
	if comment != "" && lnurl.CommentAllowed > 0 {
		if int64(len(comment)) > lnurl.CommentAllowed {
			comment = comment[:lnurl.CommentAllowed]
		}
		q.Set("comment", comment)
	}
	parsed.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Invoice{}, err
	}

	resp, err := providerClient.Do(req)
	if err != nil {
		return Invoice{}, err
	}
	defer resp.Body.Close()

	var payload InvoiceResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxLNURLResponse)).Decode(&payload); err != nil {
		return Invoice{}, err
	}
	if payload.Status == "ERROR" {
		return Invoice{}, errors.New(payload.Reason)
	}
	pr := payload.PR
	if pr == "" {
		pr = payload.PaymentRequest
	}
	pr = strings.TrimSpace(pr)
	if pr != "" {
		// The invoice is shown in guests' browsers. Accept only the bech32
		// form so a provider cannot pass markup or other text through.
		if len(pr) > 8192 || !invoicePattern.MatchString(pr) {
			return Invoice{}, errors.New("provider returned a malformed invoice")
		}
		return Invoice{PaymentRequest: pr, VerifyURL: cleanVerifyURL(payload.Verify)}, nil
	}
	return Invoice{}, errors.New("invoice not found")
}

func splitAddress(address string) (string, string, bool) {
	parts := strings.Split(address, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
