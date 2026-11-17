package ocr

import (
	"strconv"
	"strings"
	"unicode"
)

// ParsePrintedAmount reads a price as printed on a receipt and returns cents
// (or the major unit × 100 for currencies that have no minor unit).
func ParsePrintedAmount(s, currency string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if integerCurrency(currency) {
		if n, ok := parseIntegerPrice(s); ok {
			return n, true
		}
		if n, ok := parseDecimalPrice(s); ok {
			return n, true
		}
		return 0, false
	}
	if n, ok := parseDecimalPrice(s); ok {
		return n, true
	}
	if n, ok := parseWholeMajor(s); ok {
		return n, true
	}
	// Vision models often omit the currency symbol. A short whole number is
	// the major unit. OCR column detection does not use this path.
	cleaned := stripCurrency(normMoney(s))
	if allDigits(cleaned) && len(cleaned) <= 6 {
		n, err := strconv.ParseInt(cleaned, 10, 64)
		if err == nil && n > 0 {
			return n * 100, true
		}
	}
	return 0, false
}

// integerCurrency is true for units that are normally printed without a
// decimal part. COP is included because peso prices are almost always whole.
func integerCurrency(code string) bool {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "JPY", "KRW", "VND", "HUF", "CLP", "PYG", "ISK", "COP":
		return true
	default:
		return false
	}
}

type amountMode int

const (
	modeDecimal amountMode = iota
	modeInteger
)

func modeForCurrency(currency string) amountMode {
	if integerCurrency(currency) {
		return modeInteger
	}
	return modeDecimal
}

func normMoney(s string) string {
	s = strings.TrimSpace(s)
	if n, ok := normalizePriceToken(s); ok {
		return n
	}
	return s
}

// normalizePriceToken rewrites OCR confusions (O/0, l/1, …) on tokens that
// are already mostly numeric. Real words are left untouched.
func normalizePriceToken(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 20 {
		return s, false
	}
	var b strings.Builder
	rawDigits := 0
	otherLetter := 0
	for _, r := range s {
		switch r {
		case 'O', 'o', 'Q':
			b.WriteByte('0')
		case 'l', 'I', '|':
			b.WriteByte('1')
		case 'S':
			b.WriteByte('5')
		case 'B':
			b.WriteByte('8')
		case 'Z', 'z':
			b.WriteByte('2')
		case 'G':
			b.WriteByte('6')
		default:
			b.WriteRune(r)
			if unicode.IsLetter(r) {
				otherLetter++
			}
		}
		if r >= '0' && r <= '9' {
			rawDigits++
		}
	}
	if otherLetter > 0 {
		return s, false
	}
	out := b.String()
	if rawDigits == 0 && !strings.ContainsAny(out, ".,") {
		return s, false
	}
	return out, true
}

func stripCurrency(s string) string {
	s = strings.TrimSpace(s)
	codes := []string{
		"USD", "EUR", "GBP", "JPY", "HUF", "KRW", "CAD", "AUD", "CHF",
		"MXN", "DOP", "BRL", "THB", "COP", "CNY", "SEK", "NOK", "DKK", "PLN",
	}
	up := strings.ToUpper(s)
	if strings.HasPrefix(up, "RD$") {
		s = strings.TrimSpace(s[3:])
		up = strings.ToUpper(s)
	}
	for _, code := range codes {
		if strings.HasPrefix(up, code) {
			s = strings.TrimSpace(s[len(code):])
			up = strings.ToUpper(s)
		}
		if strings.HasSuffix(up, code) && len(s) >= len(code) {
			s = strings.TrimSpace(s[:len(s)-len(code)])
			up = strings.ToUpper(s)
		}
	}
	for _, sym := range []string{"$", "€", "£", "¥", "₫", "Ft", "ft", "FT", "Kč", "zł", "kr"} {
		s = strings.TrimPrefix(s, sym)
		s = strings.TrimSuffix(s, sym)
		s = strings.TrimSpace(s)
	}
	return s
}

func hasCurrencyMark(s string) bool {
	if strings.ContainsAny(s, "$€£¥₫") {
		return true
	}
	up := strings.ToUpper(strings.TrimSpace(s))
	for _, code := range []string{"USD", "EUR", "GBP", "JPY", "HUF", "KRW", "DOP", "FT", "KR"} {
		var rest string
		switch {
		case strings.HasPrefix(up, code):
			rest = up[len(code):]
		case strings.HasSuffix(up, code) && len(up) > len(code):
			rest = up[:len(up)-len(code)]
		default:
			continue
		}
		rest = strings.Trim(rest, " .,")
		if rest == "" {
			return true
		}
		rest = strings.NewReplacer(".", "", ",", "", " ", "").Replace(rest)
		if allDigits(rest) {
			return true
		}
	}
	return false
}

// parseDecimalPrice interprets s as a money amount with one or two decimal
// places. Handles US "1,234.56" and EU "1.234,56" by inspecting which
// separator appears last.
func parseDecimalPrice(s string) (int64, bool) {
	s = stripCurrency(normMoney(s))
	if s == "" {
		return 0, false
	}
	lastDot := strings.LastIndex(s, ".")
	lastComma := strings.LastIndex(s, ",")
	if lastDot < 0 && lastComma < 0 {
		return 0, false
	}
	var decSep, thouSep string
	if lastDot > lastComma {
		decSep, thouSep = ".", ","
	} else {
		decSep, thouSep = ",", "."
	}
	cleaned := strings.ReplaceAll(s, thouSep, "")
	cleaned = strings.TrimSpace(cleaned)
	parts := strings.Split(cleaned, decSep)
	if len(parts) != 2 || (len(parts[1]) != 1 && len(parts[1]) != 2) {
		return 0, false
	}
	if parts[0] != "" && !allDigits(parts[0]) {
		return 0, false
	}
	if !allDigits(parts[1]) {
		return 0, false
	}
	var dollars int64
	if parts[0] != "" {
		var err error
		dollars, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || dollars < 0 || dollars > 999_999 {
			return 0, false
		}
	}
	cents, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || cents < 0 || cents > 99 {
		return 0, false
	}
	if len(parts[1]) == 1 {
		cents *= 10
	}
	return dollars*100 + cents, true
}

// parseIntegerPrice interprets s as a whole-number money amount, for
// currencies without a minor unit. The value is stored × 100 so the rest of
// the pipeline can keep using cents. At least three digits are required so
// quantities and page numbers stay out of the price column.
func parseIntegerPrice(s string) (int64, bool) {
	s = stripCurrency(normMoney(s))
	if s == "" {
		return 0, false
	}
	cleaned := strings.NewReplacer(" ", "", ",", "", ".", "", "'", "", " ", "").Replace(s)
	if !allDigits(cleaned) || len(cleaned) < 3 || len(cleaned) > 9 {
		return 0, false
	}
	n, err := strconv.ParseInt(cleaned, 10, 64)
	if err != nil || n < 1 {
		return 0, false
	}
	return n * 100, true
}

// parseWholeMajor accepts a currency-marked whole amount such as "$12".
// Bare integers are rejected so order numbers are not treated as prices.
func parseWholeMajor(s string) (int64, bool) {
	raw := strings.TrimSpace(s)
	if !hasCurrencyMark(raw) {
		return 0, false
	}
	cleaned := stripCurrency(normMoney(raw))
	if !allDigits(cleaned) || len(cleaned) < 1 || len(cleaned) > 4 {
		return 0, false
	}
	n, err := strconv.ParseInt(cleaned, 10, 64)
	if err != nil || n < 1 || n > 9999 {
		return 0, false
	}
	return n * 100, true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isJustCurrency(s string) bool {
	switch strings.TrimSpace(s) {
	case "$", "€", "£", "¥", "₫",
		"USD", "EUR", "GBP", "JPY", "HUF", "KRW", "CAD", "AUD", "CHF",
		"MXN", "DOP", "BRL", "THB", "COP", "RD$", "rd$", "RD", "Ft", "ft", "FT", "kr", "KR":
		return true
	default:
		return false
	}
}

var footerExact = map[string]bool{
	"subtotal": true, "sub-total": true, "total": true, "totals": true,
	"tax": true, "taxes": true, "tip": true, "tips": true, "gratuity": true,
	"change": true, "cash": true, "card": true, "credit": true, "debit": true,
	"visa": true, "mastercard": true, "amex": true, "discover": true,
	"balance": true, "due": true, "paid": true, "vat": true, "gst": true,
	"hst": true, "pst": true,
	"iva": true, "itbis": true, "ley": true, "propina": true, "cambio": true, "efectivo": true, "tarjeta": true,
	"mwst": true, "ust": true, "summe": true, "gesamt": true, "trinkgeld": true,
	"tva": true, "pourboire": true, "espèces": true, "especes": true, "carte": true,
	"mancia": true, "grazie": true, "totale": true, "contanti": true,
	"gorjeta": true, "obrigado": true,
	"afa": true, "áfa": true, "összesen": true, "fizetendő": true,
	"borravaló": true, "készpénz": true,
	"service charge": true, "sales tax": true, "grand total": true,
	"amount due": true, "balance due": true, "thank you": true,
}

var footerPrefixes = []string{
	"subtotal", "sub total", "sub-total", "itbis",
	"grand total", "order total", "sales tax", "service charge",
	"amount due", "balance due", "total due", "amount paid",
	"credit card", "debit card", "thank you", "change due",
}

func normalizeFooter(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, "*-_=:|/\\.,;!?()[]{}")
	return strings.Join(strings.Fields(s), " ")
}

// IsAllocatableCharge reports a receipt line whose amount should be shared
// across the table in proportion to what each person ordered. That is sales
// tax and its equivalents, including Dominican ITBIS (18% VAT) and Ley (the
// mandatory 10% service charge). A voluntary tip is not included; the host
// enters that separately.
func IsAllocatableCharge(label string) bool {
	n := normalizeFooter(label)
	if n == "" {
		return false
	}
	if strings.Contains(n, "sales tax") {
		return true
	}
	for _, w := range strings.Fields(n) {
		w = strings.Trim(w, "%")
		switch w {
		case "tax", "taxes", "vat", "gst", "hst", "pst", "iva", "itbis", "mwst", "tva", "ust", "ley":
			return true
		}
	}
	return false
}

func isFooterLabel(label string) bool {
	n := normalizeFooter(label)
	if n == "" {
		return false
	}
	if footerExact[n] {
		return true
	}
	for _, p := range footerPrefixes {
		if n == p || strings.HasPrefix(n, p+" ") {
			return true
		}
	}
	return false
}

func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "*-_=:|/\\")
	s = strings.TrimSpace(s)
	return strings.Join(strings.Fields(s), " ")
}

func isItemLine(label string) bool {
	if label == "" || isFooterLabel(label) {
		return false
	}
	for _, r := range label {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func startsWithLower(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return unicode.IsLower(r)
		}
	}
	return false
}
