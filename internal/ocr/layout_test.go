package ocr

import (
	"fmt"
	"strings"
	"testing"
)

func wordAt(text string, line, left, top, width, conf int) Word {
	if width <= 0 {
		width = 12 * len([]rune(text))
		if width < 14 {
			width = 14
		}
	}
	if conf == 0 {
		conf = 90
	}
	return Word{
		BlockNum: 1,
		ParaNum:  1,
		LineNum:  line,
		Left:     left,
		Top:      top,
		Width:    width,
		Height:   22,
		Conf:     conf,
		Text:     text,
	}
}

func tsvOf(words []Word) string {
	var b strings.Builder
	b.WriteString("level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n")
	for i, w := range words {
		fmt.Fprintf(&b, "5\t1\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			w.BlockNum, w.ParaNum, w.LineNum, i+1, w.Left, w.Top, w.Width, w.Height, w.Conf, w.Text)
	}
	return b.String()
}

func labels(res Result) []string {
	out := make([]string, len(res.Items))
	for i, item := range res.Items {
		out[i] = fmt.Sprintf("%s=%d", item.Label, item.AmountCents)
	}
	return out
}

func TestExtractSplitsPriceFragments(t *testing.T) {
	words := []Word{
		wordAt("Burger", 1, 20, 100, 90, 90),
		wordAt("$", 1, 400, 100, 14, 40),
		wordAt("12", 1, 418, 100, 28, 40),
		wordAt(".", 1, 448, 100, 8, 20),
		wordAt("50", 1, 458, 100, 28, 35),
		wordAt("Fries", 2, 20, 140, 70, 90),
		wordAt("4.25", 2, 440, 140, 46, 88),
		wordAt("Total", 3, 20, 190, 70, 90),
		wordAt("16.75", 3, 430, 190, 56, 90),
	}
	res := ExtractFromTSVCurrency(tsvOf(words), "USD")
	got := labels(res)
	want := []string{"Burger=1250", "Fries=425"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("items = %v", got)
	}
	if !res.Stats.ColumnFound || res.Stats.PriceFormat != "decimal" {
		t.Fatalf("stats = %+v", res.Stats)
	}
}

func TestExtractMergesPriceOntoSameVisualLine(t *testing.T) {
	// Tesseract gave the price its own line id, but the boxes share a row.
	words := []Word{
		wordAt("Latte", 1, 20, 200, 70, 90),
		wordAt("4.75", 2, 440, 204, 50, 80),
	}
	res := ExtractFromTSVCurrency(tsvOf(words), "USD")
	if len(res.Items) != 1 || res.Items[0].Label != "Latte" || res.Items[0].AmountCents != 475 {
		t.Fatalf("items = %+v", res.Items)
	}
}

func TestExtractKeepsLowConfidencePriceAndFloatConf(t *testing.T) {
	raw := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"5\t1\t1\t1\t1\t1\t20\t80\t80\t22\t93.7\tSoup\n" +
		"5\t1\t1\t1\t1\t2\t430\t80\t50\t22\t8.4\tl2.5O\n"
	res := ExtractFromTSVCurrency(raw, "USD")
	if len(res.Items) != 1 {
		t.Fatalf("items = %+v text=%q", res.Items, res.Text)
	}
	if res.Items[0].Label != "Soup" || res.Items[0].AmountCents != 1250 {
		t.Fatalf("item = %+v", res.Items[0])
	}
}

func TestExtractDoesNotMultiplyLeadingNumbers(t *testing.T) {
	words := []Word{
		wordAt("12", 1, 20, 100, 28, 90),
		wordAt("oz", 1, 52, 100, 28, 90),
		wordAt("Soda", 1, 86, 100, 60, 90),
		wordAt("3.00", 1, 440, 100, 50, 90),
		wordAt("2x", 2, 20, 140, 30, 90),
		wordAt("Coke", 2, 56, 140, 50, 90),
		wordAt("5.00", 2, 440, 140, 50, 90),
	}
	res := ExtractFromTSVCurrency(tsvOf(words), "USD")
	if len(res.Items) != 2 {
		t.Fatalf("items = %+v", res.Items)
	}
	if res.Items[0].Label != "12 oz Soda" || res.Items[0].AmountCents != 300 {
		t.Fatalf("first = %+v", res.Items[0])
	}
	if res.Items[1].Label != "2x Coke" || res.Items[1].AmountCents != 500 {
		t.Fatalf("second = %+v", res.Items[1])
	}
}

func TestExtractFooterVersusRealItems(t *testing.T) {
	words := []Word{
		wordAt("Room", 1, 20, 100, 50, 90),
		wordAt("Service", 1, 76, 100, 70, 90),
		wordAt("15.00", 1, 430, 100, 56, 90),
		wordAt("Service", 2, 20, 140, 70, 90),
		wordAt("Charge", 2, 96, 140, 70, 90),
		wordAt("3.00", 2, 440, 140, 46, 90),
		wordAt("Sales", 3, 20, 180, 50, 90),
		wordAt("Tax", 3, 76, 180, 40, 90),
		wordAt("1.20", 3, 450, 180, 40, 90),
	}
	res := ExtractFromTSVCurrency(tsvOf(words), "USD")
	if len(res.Items) != 1 || res.Items[0].Label != "Room Service" || res.Items[0].AmountCents != 1500 {
		t.Fatalf("items = %+v", res.Items)
	}
	if res.TaxCents != 120 {
		t.Fatalf("tax = %d", res.TaxCents)
	}
}

func TestExtractSplitsDominicanTax(t *testing.T) {
	words := []Word{
		wordAt("Mofongo", 1, 20, 100, 90, 90),
		wordAt("500.00", 1, 400, 100, 70, 90),
		wordAt("ITBIS", 2, 20, 140, 60, 90),
		wordAt("90.00", 2, 420, 140, 60, 90),
		wordAt("10%", 3, 20, 180, 40, 90),
		wordAt("Ley", 3, 66, 180, 40, 90),
		wordAt("50.00", 3, 420, 180, 60, 90),
		wordAt("Total", 4, 20, 220, 60, 90),
		wordAt("640.00", 4, 410, 220, 70, 90),
	}
	res := ExtractFromTSVCurrency(tsvOf(words), "DOP")
	if len(res.Items) != 1 || res.Items[0].Label != "Mofongo" || res.Items[0].AmountCents != 50000 {
		t.Fatalf("items = %+v", res.Items)
	}
	if res.TaxCents != 14000 {
		t.Fatalf("tax = %d", res.TaxCents)
	}
}

func TestExtractKeepsSlantedBareAmounts(t *testing.T) {
	words := []Word{
		wordAt("Mofongo", 1, 20, 100, 90, 90),
		wordAt("500", 1, 180, 100, 40, 90),
		wordAt("Jugo", 2, 30, 160, 50, 90),
		wordAt("150", 2, 520, 160, 40, 90),
		wordAt("ITBIS", 3, 20, 220, 60, 90),
		wordAt("90", 3, 300, 220, 30, 90),
		wordAt("Total", 4, 20, 280, 60, 90),
		wordAt("740", 4, 400, 280, 40, 90),
	}
	res := ExtractFromTSVCurrency(tsvOf(words), "DOP")
	if len(res.Items) != 2 {
		t.Fatalf("items = %+v format=%s", res.Items, res.Stats.PriceFormat)
	}
	if res.Items[0].Label != "Mofongo" || res.Items[0].AmountCents != 50000 {
		t.Fatalf("first = %+v", res.Items[0])
	}
	if res.Items[1].Label != "Jugo" || res.Items[1].AmountCents != 15000 {
		t.Fatalf("second = %+v", res.Items[1])
	}
	if res.TaxCents != 9000 {
		t.Fatalf("tax = %d", res.TaxCents)
	}
}

func TestExtractWrapsContinuationLine(t *testing.T) {
	words := []Word{
		wordAt("Caesar", 1, 20, 80, 80, 90),
		wordAt("with", 2, 20, 112, 50, 90),
		wordAt("chicken", 2, 76, 112, 80, 90),
		wordAt("14.50", 2, 430, 112, 56, 90),
	}
	res := ExtractFromTSVCurrency(tsvOf(words), "USD")
	if len(res.Items) != 1 || res.Items[0].Label != "Caesar with chicken" || res.Items[0].AmountCents != 1450 {
		t.Fatalf("items = %+v", res.Items)
	}
}

func TestExtractDoesNotAbsorbMerchantName(t *testing.T) {
	words := []Word{
		wordAt("Blue", 1, 20, 40, 50, 90),
		wordAt("Bottle", 1, 76, 40, 70, 90),
		wordAt("Latte", 2, 20, 100, 60, 90),
		wordAt("4.50", 2, 440, 100, 50, 90),
	}
	res := ExtractFromTSVCurrency(tsvOf(words), "USD")
	if len(res.Items) != 1 || res.Items[0].Label != "Latte" {
		t.Fatalf("items = %+v", res.Items)
	}
}

func TestExtractEuroCommaAndWholeDollars(t *testing.T) {
	euro := []Word{
		wordAt("Cafe", 1, 20, 100, 50, 90),
		wordAt("3,50", 1, 440, 100, 50, 90),
		wordAt("Eau", 2, 20, 140, 40, 90),
		wordAt("1,20", 2, 440, 140, 50, 90),
	}
	res := ExtractFromTSVCurrency(tsvOf(euro), "EUR")
	if len(res.Items) != 2 || res.Items[0].AmountCents != 350 || res.Items[1].AmountCents != 120 {
		t.Fatalf("euro = %+v", res.Items)
	}

	whole := []Word{
		wordAt("Coffee", 1, 20, 100, 70, 90),
		wordAt("$4", 1, 450, 100, 30, 90),
		wordAt("Tea", 2, 20, 140, 40, 90),
		wordAt("$3", 2, 450, 140, 30, 90),
	}
	res = ExtractFromTSVCurrency(tsvOf(whole), "USD")
	if len(res.Items) != 2 || res.Items[0].AmountCents != 400 || res.Items[1].AmountCents != 300 {
		t.Fatalf("whole = %+v", res.Items)
	}
}

func TestExtractIgnoresBareOrderNumbersForUSD(t *testing.T) {
	words := []Word{
		wordAt("Ticket", 1, 20, 100, 70, 90),
		wordAt("101", 1, 450, 100, 40, 90),
		wordAt("Ticket", 2, 20, 140, 70, 90),
		wordAt("102", 2, 450, 140, 40, 90),
		wordAt("Ticket", 3, 20, 180, 70, 90),
		wordAt("103", 3, 450, 180, 40, 90),
	}
	res := ExtractFromTSVCurrency(tsvOf(words), "USD")
	if len(res.Items) != 0 {
		t.Fatalf("items = %+v", res.Items)
	}
}

func TestExtractIntegerCurrency(t *testing.T) {
	words := []Word{
		wordAt("Espresso", 1, 20, 100, 90, 90),
		wordAt("450", 1, 430, 100, 50, 90),
		wordAt("Tea", 2, 20, 140, 40, 90),
		wordAt("1.200", 2, 420, 140, 60, 90),
	}
	res := ExtractFromTSVCurrency(tsvOf(words), "HUF")
	if len(res.Items) != 2 {
		t.Fatalf("items = %+v", res.Items)
	}
	if res.Items[0].AmountCents != 45000 || res.Items[1].AmountCents != 120000 {
		t.Fatalf("cents = %d %d", res.Items[0].AmountCents, res.Items[1].AmountCents)
	}
	if res.Stats.PriceFormat != "integer" {
		t.Fatalf("format = %s", res.Stats.PriceFormat)
	}
}

func TestParsePrintedAmount(t *testing.T) {
	cases := []struct {
		in, currency string
		cents        int64
		ok           bool
	}{
		{"RD$12.50", "DOP", 1250, true},
		{"DOP 350.00", "DOP", 35000, true},
		{"12.50", "USD", 1250, true},
		{"$12.5", "USD", 1250, true},
		{"1,234.56", "USD", 123456, true},
		{"3,50", "EUR", 350, true},
		{"12", "USD", 1200, true},
		{"2000", "USD", 200000, true},
		{"450", "HUF", 45000, true},
		{"1.250", "HUF", 125000, true},
		{"l2.5O", "USD", 1250, true},
	}
	for _, tc := range cases {
		got, ok := ParsePrintedAmount(tc.in, tc.currency)
		if ok != tc.ok || got != tc.cents {
			t.Errorf("ParsePrintedAmount(%q, %s) = %d, %v", tc.in, tc.currency, got, ok)
		}
	}
}

func TestParseOSD(t *testing.T) {
	text := "Page number: 0\nOrientation in degrees: 90\nRotate: 270\nOrientation confidence: 11.25\nScript: Latin\n"
	deg, conf, err := parseOSD(text)
	if err != nil || deg != 270 || conf < 11 || conf > 12 {
		t.Fatalf("osd = %d %.2f %v", deg, conf, err)
	}
}
