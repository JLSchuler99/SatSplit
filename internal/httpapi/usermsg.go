package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"satsplit/internal/store"
	"satsplit/internal/vision"
)

// Sentences shown to people. Internal errors stay in the server log.
const (
	msgNotImage      = "That file is not a photo SatSplit can read. Use a JPEG, PNG, WebP or HEIC image."
	msgTooLarge      = "That photo is too large. Use one under 20 MB."
	msgMissingFile   = "Choose a photo of the receipt and try again."
	msgMissingAddr   = "Enter your Lightning address first. It looks like name@domain.com."
	msgBadAddress    = "That Lightning address doesn't look right. It should look like name@domain.com. Double-check the spelling and try again."
	msgIncomplete    = "The upload was incomplete. Choose the photo and try again."
	msgSaveFailed    = "We couldn't save this receipt. Wait a moment and try again."
	msgOCRFailed     = "We couldn't read this photo. Try a clearer, well-lit picture of the whole receipt."
	msgNoItems       = "We couldn't read any items from this photo. Try a clearer, well-lit picture of the whole receipt, or type the items in below."
	msgReadTimeout   = "Reading this receipt took too long and didn't finish. Type the items in below, or try the photo again in a moment. If it already shows up under Your receipts, open that one instead of uploading again."
	msgReaderDown    = "The receipt reader is unavailable right now. Type the items in below, or try the photo again in a few minutes."
	msgLNUnreachable = "We couldn't reach the domain in that Lightning address. It may be misspelled, or that domain doesn't serve a Lightning address. Double-check the spelling and try again."
	msgLNNotServed   = "That address doesn't serve a Lightning address we can pay. Double-check the spelling, and make sure the domain hosts a Lightning address."
	msgInvoiceFail   = "The Lightning address didn't return an invoice. Try again in a moment."
	msgRateFailed    = "We couldn't look up the bitcoin price just now. Try again in a moment."
)

// readOutcomeMessage is what the host sees after a vision pass. reading is
// true while the pass is still going, so the page can show its own progress
// line instead of a result.
func readOutcomeMessage(issue string, checked bool, diff int64, currency string, reading bool) string {
	if reading {
		return ""
	}
	switch issue {
	case store.ReadNoItems:
		return msgNoItems
	case store.ReadTimeout:
		return msgReadTimeout
	case store.ReadUnavailable:
		return msgReaderDown
	case "":
		if checked && diff != 0 {
			return offByMessage(diff, currency)
		}
		return ""
	default:
		// An unexpected issue must not be echoed; it may be a raw error.
		return msgReaderDown
	}
}

func offByMessage(diff int64, currency string) string {
	direction := "more"
	if diff < 0 {
		direction = "less"
	}
	if diff < 0 {
		diff = -diff
	}
	return fmt.Sprintf(
		"The items we read add up to %s %s than the printed total. Compare them with the photo and correct anything that's wrong before you share.",
		formatMoney(diff, currency), direction,
	)
}

func formatMoney(cents int64, currency string) string {
	if cents < 0 {
		cents = -cents
	}
	amount := fmt.Sprintf("%d.%02d", cents/100, cents%100)
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "", "USD":
		return "$" + amount
	case "EUR":
		return "€" + amount
	case "GBP":
		return "£" + amount
	default:
		return amount + " " + strings.ToUpper(strings.TrimSpace(currency))
	}
}

// classifyVisionErr maps a vision failure onto a stored issue. The original
// error, which can name a model host, stays in the log.
func classifyVisionErr(err error) string {
	if err == nil {
		return store.ReadNoItems
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return store.ReadTimeout
	}
	if onlyNoItems(err) {
		return store.ReadNoItems
	}
	return store.ReadUnavailable
}

func onlyNoItems(err error) bool {
	type joined interface{ Unwrap() []error }
	if j, ok := err.(joined); ok {
		parts := j.Unwrap()
		if len(parts) == 0 {
			return false
		}
		for _, part := range parts {
			if !onlyNoItems(part) {
				return false
			}
		}
		return true
	}
	return errors.Is(err, vision.ErrNoItems)
}

// lightningLookupMessage explains a failed Lightning address lookup without
// including the provider URL or the DNS error text.
func lightningLookupMessage(err error) string {
	if err == nil {
		return msgLNUnreachable
	}
	if err.Error() == "invalid lightning address" {
		return msgBadAddress
	}
	if hostUnreachable(err) {
		return msgLNUnreachable
	}
	return msgLNNotServed
}

func hostUnreachable(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	low := strings.ToLower(err.Error())
	for _, needle := range []string{
		"no such host",
		"timeout",
		"timed out",
		"connection refused",
		"connection reset",
		"network is unreachable",
		"no route to host",
		"certificate",
		"tls handshake",
		"server misbehaving",
	} {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}

// cleanUploadID accepts the id the browser sends so a retry of the same
// upload finds the receipt already stored. Anything else is ignored.
func cleanUploadID(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 16 || len(raw) > 64 {
		return ""
	}
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return ""
		}
	}
	return raw
}
