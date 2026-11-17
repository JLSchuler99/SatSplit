package ocr

import (
	"bufio"
	"sort"
	"strconv"
	"strings"

	"satsplit/internal/store"
)

// Word is a single OCR token with its bounding box and recognition confidence
// as reported by Tesseract's TSV output.
type Word struct {
	BlockNum int
	ParaNum  int
	LineNum  int
	WordNum  int
	Left     int
	Top      int
	Width    int
	Height   int
	Conf     int
	Text     string
}

func (w Word) Right() int  { return w.Left + w.Width }
func (w Word) Bottom() int { return w.Top + w.Height }

// Line is a horizontal cluster of words.
type Line struct {
	Words  []Word
	Top    int
	Bottom int
}

// Stats describes what the parser saw and decided.
type Stats struct {
	WordCount       int
	PriceCandidates int
	ColumnFound     bool
	ColumnTightness float64
	PriceFormat     string
	Pass            string
}

// Result is the structured output of an OCR pass.
type Result struct {
	Items []store.Item
	// TaxCents is the sum of sales tax, ITBIS, Ley, and similar charges.
	// It is split by food share instead of being claimed as a line item.
	TaxCents   int64
	Text       string
	Confidence float64
	Stats      Stats
}

// ParseTSV decodes Tesseract's TSV output into word boxes. Confidence may be
// an integer or a float depending on the Tesseract build; both are accepted.
// Low-confidence tokens are kept when they still look like a price fragment,
// because the price column is often the least confident part of a photo.
func ParseTSV(tsv string) []Word {
	var words []Word
	sc := bufio.NewScanner(strings.NewReader(tsv))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 12 {
			continue
		}
		level, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil || level != 5 {
			continue
		}
		text := strings.TrimRight(fields[11], "\r")
		if strings.TrimSpace(text) == "" {
			continue
		}
		conf := parseConf(fields[10])
		if !keepToken(text, conf) {
			continue
		}
		words = append(words, Word{
			BlockNum: atoi(fields[2]),
			ParaNum:  atoi(fields[3]),
			LineNum:  atoi(fields[4]),
			WordNum:  atoi(fields[5]),
			Left:     atoi(fields[6]),
			Top:      atoi(fields[7]),
			Width:    atoi(fields[8]),
			Height:   atoi(fields[9]),
			Conf:     conf,
			Text:     text,
		})
	}
	return words
}

func keepToken(text string, conf int) bool {
	if conf < 0 {
		return false
	}
	if conf >= 15 {
		return true
	}
	t := strings.TrimSpace(text)
	if t == "." || t == "," || isJustCurrency(t) {
		return true
	}
	_, ok := normalizePriceToken(t)
	return ok
}

func parseConf(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(f)
}

// GroupLines clusters words by Tesseract's (block, paragraph, line) key and
// sorts within each line by horizontal position.
func GroupLines(words []Word) []Line {
	type key struct{ b, p, l int }
	buckets := map[key][]Word{}
	for _, w := range words {
		k := key{w.BlockNum, w.ParaNum, w.LineNum}
		buckets[k] = append(buckets[k], w)
	}
	lines := make([]Line, 0, len(buckets))
	for _, ws := range buckets {
		sort.Slice(ws, func(i, j int) bool { return ws[i].Left < ws[j].Left })
		top, bottom := ws[0].Top, ws[0].Bottom()
		for _, w := range ws {
			if w.Top < top {
				top = w.Top
			}
			if w.Bottom() > bottom {
				bottom = w.Bottom()
			}
		}
		lines = append(lines, Line{Words: ws, Top: top, Bottom: bottom})
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].Top < lines[j].Top })
	return lines
}

// MergeLinesByY joins tokens Tesseract split across line ids even though they
// sit on the same visual row. Receipt prices are often far to the right of
// the label and get their own line number.
func MergeLinesByY(lines []Line) []Line {
	if len(lines) < 2 {
		return lines
	}
	sorted := append([]Line(nil), lines...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Top < sorted[j].Top })
	med := medianLineHeight(sorted)
	if med < 8 {
		med = 8
	}
	thresh := med / 2
	if thresh < 6 {
		thresh = 6
	}

	var out []Line
	cur := sorted[0]
	mid := (cur.Top + cur.Bottom) / 2
	for _, l := range sorted[1:] {
		lm := (l.Top + l.Bottom) / 2
		if abs(lm-mid) <= thresh {
			cur.Words = append(cur.Words, l.Words...)
			if l.Top < cur.Top {
				cur.Top = l.Top
			}
			if l.Bottom > cur.Bottom {
				cur.Bottom = l.Bottom
			}
			mid = (cur.Top + cur.Bottom) / 2
			continue
		}
		sortWords(cur.Words)
		out = append(out, cur)
		cur = l
		mid = (cur.Top + cur.Bottom) / 2
	}
	sortWords(cur.Words)
	out = append(out, cur)
	return out
}

func sortWords(ws []Word) {
	sort.Slice(ws, func(i, j int) bool { return ws[i].Left < ws[j].Left })
}

// JoinText reconstructs a plain-text representation of the OCR pass.
func JoinText(lines []Line) string {
	var buf strings.Builder
	for _, l := range lines {
		parts := make([]string, len(l.Words))
		for i, w := range l.Words {
			parts[i] = w.Text
		}
		buf.WriteString(strings.Join(parts, " "))
		buf.WriteByte('\n')
	}
	return strings.TrimRight(buf.String(), "\n")
}

// ExtractFromTSV runs the layout-aware parser. currency selects decimal
// prices (USD, EUR, …) or whole-unit prices (HUF, JPY, …). An empty currency
// is treated as decimal.
func ExtractFromTSV(tsv string) Result {
	return ExtractFromTSVCurrency(tsv, "")
}

// ExtractFromTSVCurrency is ExtractFromTSV with an explicit currency code.
func ExtractFromTSVCurrency(tsv, currency string) Result {
	words := ParseTSV(tsv)
	lines := MergeLinesByY(GroupLines(words))
	mode := modeForCurrency(currency)
	for i := range lines {
		lines[i].Words = stitchPrices(lines[i].Words, mode)
	}
	text := JoinText(lines)
	stats := Stats{WordCount: len(words), PriceFormat: "none"}
	if len(words) == 0 {
		return Result{Text: text, Stats: stats}
	}

	imgW := imageWidth(words)
	col, ok := detectPriceColumn(lines, imgW, mode)
	if !ok {
		items, taxCents, candidates := looseItems(lines, mode, allowBareAmount(currency))
		stats.PriceCandidates = candidates
		stats.PriceFormat = "loose"
		return Result{
			Items:      items,
			TaxCents:   taxCents,
			Text:       text,
			Confidence: confidenceScore(items, words, candidates, false, 0),
			Stats:      stats,
		}
	}
	stats.ColumnFound = true
	stats.ColumnTightness = col.tightness
	stats.PriceFormat = col.format

	type row struct {
		line   Line
		label  string
		cents  int64
		priced bool
	}
	rows := make([]row, 0, len(lines))
	priceCandidates := 0
	for _, line := range lines {
		cents, priceLeft, priced := linePrice(line, col)
		if priced {
			priceCandidates++
		}
		label := lineLabel(line, priceLeft, priced, col.parse)
		rows = append(rows, row{line: line, label: label, cents: cents, priced: priced})
	}
	stats.PriceCandidates = priceCandidates

	var items []store.Item
	var taxCents int64
	for i, row := range rows {
		if !row.priced {
			continue
		}
		label := row.label
		if i > 0 && !rows[i-1].priced && isItemLine(rows[i-1].label) {
			if isContinuation(rows[i-1].line, row.line, label) {
				label = cleanLabel(rows[i-1].label + " " + label)
			}
		}
		if IsAllocatableCharge(label) {
			taxCents += row.cents
			continue
		}
		if !isItemLine(label) {
			continue
		}
		items = append(items, store.Item{
			Label:       label,
			AmountCents: row.cents,
			SplitCount:  1,
		})
	}
	if len(items) == 0 {
		loose, looseTax, looseN := looseItems(lines, mode, allowBareAmount(currency))
		if len(loose) > 0 {
			items = loose
			taxCents = looseTax
			stats.PriceFormat = "loose"
			if looseN > stats.PriceCandidates {
				stats.PriceCandidates = looseN
			}
		}
	}

	return Result{
		Items:      items,
		TaxCents:   taxCents,
		Text:       text,
		Confidence: confidenceScore(items, words, priceCandidates, true, col.tightness),
		Stats:      stats,
	}
}

// strongEnough reports whether another OCR pass is unlikely to help.
func (r Result) strongEnough() bool {
	if !r.Stats.ColumnFound || len(r.Items) == 0 || r.Confidence < 0.62 {
		return false
	}
	// A long receipt that collapsed to a single row is usually a bad line split.
	if len(r.Items) == 1 && r.Stats.WordCount > 40 && r.Stats.PriceCandidates > 3 {
		return false
	}
	return true
}

type priceParser func(string) (int64, bool)

type priceColumn struct {
	rightX    int
	tolerance int
	tightness float64
	parse     priceParser
	format    string
}

func detectPriceColumn(lines []Line, imgWidth int, mode amountMode) (priceColumn, bool) {
	med := medianWordHeight(lines)
	tolerance := med * 2
	if tolerance < 12 {
		tolerance = 12
	}
	if imgWidth > 0 && tolerance > imgWidth/8 {
		tolerance = imgWidth / 8
	}
	if tolerance < 12 {
		tolerance = 12
	}

	if mode == modeInteger {
		if col, ok := findColumn(lines, tolerance, parseIntegerPrice, true); ok {
			col.parse = parseIntegerPrice
			col.format = "integer"
			return col, true
		}
		return priceColumn{}, false
	}
	if col, ok := findColumn(lines, tolerance, parseDecimalPrice, false); ok {
		col.parse = parseDecimalPrice
		col.format = "decimal"
		return col, true
	}
	if col, ok := findColumn(lines, tolerance, parseWholeMajor, true); ok {
		col.parse = parseWholeMajor
		col.format = "whole"
		return col, true
	}
	return priceColumn{}, false
}

func findColumn(lines []Line, tolerance int, parse priceParser, rightmostOnly bool) (priceColumn, bool) {
	var rights []int
	for _, l := range lines {
		if rightmostOnly {
			w, ok := lastContentWord(l)
			if !ok {
				continue
			}
			if _, ok := parse(w.Text); ok {
				rights = append(rights, w.Right())
			}
			continue
		}
		for _, w := range l.Words {
			if _, ok := parse(w.Text); ok {
				rights = append(rights, w.Right())
			}
		}
	}
	if len(rights) == 0 {
		return priceColumn{}, false
	}
	mean, n, ok := densestRight(rights, tolerance)
	if !ok {
		return priceColumn{}, false
	}
	return priceColumn{
		rightX:    mean,
		tolerance: tolerance,
		tightness: float64(n) / float64(len(rights)),
	}, true
}

// densestRight finds the rightmost window of right-edge positions whose
// width is at most tolerance. Clustering by consecutive gaps merges a slanted
// column into one blob; a fixed window does not.
func densestRight(rights []int, tolerance int) (mean, count int, ok bool) {
	if len(rights) == 0 {
		return 0, 0, false
	}
	sorted := append([]int(nil), rights...)
	sort.Ints(sorted)
	bestN := 0
	bestMean := 0
	for i := range sorted {
		j := i
		for j < len(sorted) && sorted[j]-sorted[i] <= tolerance {
			j++
		}
		n := j - i
		sum := 0
		for k := i; k < j; k++ {
			sum += sorted[k]
		}
		m := sum / n
		if n > bestN || (n == bestN && m > bestMean) {
			bestN = n
			bestMean = m
		}
	}
	return bestMean, bestN, bestN > 0
}

func lastContentWord(line Line) (Word, bool) {
	for i := len(line.Words) - 1; i >= 0; i-- {
		if isJustCurrency(line.Words[i].Text) {
			continue
		}
		return line.Words[i], true
	}
	return Word{}, false
}

// looseItems keeps the rightmost amount on each line when the prices do not
// share a vertical column. Phone photos of receipts often slant enough that
// the column detector drops every line.
func allowBareAmount(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), "DOP")
}

func looseItems(lines []Line, mode amountMode, allowBare bool) (items []store.Item, taxCents int64, candidates int) {
	for _, line := range lines {
		cents, priceLeft, ok := trailingAmount(line, mode, allowBare)
		if !ok {
			continue
		}
		candidates++
		label := lineLabel(line, priceLeft, true, amountParser(mode))
		if IsAllocatableCharge(label) {
			taxCents += cents
			continue
		}
		if !isItemLine(label) {
			continue
		}
		items = append(items, store.Item{
			Label:       label,
			AmountCents: cents,
			SplitCount:  1,
		})
	}
	return items, taxCents, candidates
}

func amountParser(mode amountMode) priceParser {
	if mode == modeInteger {
		return parseIntegerPrice
	}
	return parseDecimalPrice
}

func trailingAmount(line Line, mode amountMode, allowBare bool) (cents int64, priceLeft int, ok bool) {
	w, found := lastContentWord(line)
	if !found {
		return 0, 0, false
	}
	if mode == modeInteger {
		if cents, ok = parseIntegerPrice(w.Text); ok {
			return cents, w.Left, true
		}
		return 0, 0, false
	}
	if cents, ok = parseDecimalPrice(w.Text); ok {
		return cents, w.Left, true
	}
	if cents, ok = parseWholeMajor(w.Text); ok {
		return cents, w.Left, true
	}
	if !allowBare {
		return 0, 0, false
	}
	raw := stripCurrency(normMoney(w.Text))
	if !allDigits(raw) || len(raw) < 2 || len(raw) > 6 {
		return 0, 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, 0, false
	}
	return n * 100, w.Left, true
}

func linePrice(line Line, col priceColumn) (cents int64, priceLeft int, ok bool) {
	for i := len(line.Words) - 1; i >= 0; i-- {
		w := line.Words[i]
		cents, parsed := col.parse(w.Text)
		if !parsed {
			continue
		}
		if abs(w.Right()-col.rightX) > col.tolerance {
			continue
		}
		return cents, w.Left, true
	}
	return 0, 0, false
}

func lineLabel(line Line, priceLeft int, priced bool, parse priceParser) string {
	var parts []string
	for _, w := range line.Words {
		if priced && w.Left >= priceLeft {
			continue
		}
		if _, isPrice := parse(w.Text); isPrice {
			continue
		}
		if isJustCurrency(w.Text) {
			continue
		}
		if w.Conf > 0 && w.Conf < 15 {
			continue
		}
		parts = append(parts, w.Text)
	}
	return cleanLabel(strings.Join(parts, " "))
}

func isContinuation(prev, cur Line, curLabel string) bool {
	if curLabel != "" && !startsWithLower(curLabel) {
		return false
	}
	if len(prev.Words) == 0 || len(cur.Words) == 0 {
		return false
	}
	h := cur.Bottom - cur.Top
	if h < 8 {
		h = 8
	}
	gap := cur.Top - prev.Bottom
	if gap > h {
		return false
	}
	if abs(prev.Words[0].Left-cur.Words[0].Left) > h*3 {
		return false
	}
	return true
}

func countPrices(lines []Line, mode amountMode) int {
	n := 0
	for _, l := range lines {
		for _, w := range l.Words {
			if _, ok := parseDecimalPrice(w.Text); ok {
				n++
				continue
			}
			if mode == modeInteger {
				if _, ok := parseIntegerPrice(w.Text); ok {
					n++
				}
			}
		}
	}
	return n
}

func stitchPrices(words []Word, mode amountMode) []Word {
	if len(words) < 2 {
		return words
	}
	h := medianWordHeight([]Line{{Words: words, Top: words[0].Top, Bottom: words[0].Bottom()}})
	maxGap := h
	if maxGap < 10 {
		maxGap = 10
	}
	var out []Word
	for i := 0; i < len(words); {
		if !glueable(words[i]) {
			out = append(out, words[i])
			i++
			continue
		}
		bestJ := -1
		bestText := ""
		for j := i; j < len(words) && j < i+6; j++ {
			if j > i {
				gap := words[j].Left - words[j-1].Right()
				if gap > maxGap || !glueable(words[j]) {
					break
				}
			}
			text, ok := spanAcceptable(words[i:j+1], mode)
			if ok {
				bestJ = j
				bestText = text
			}
		}
		if bestJ > i {
			out = append(out, mergedWord(words[i:bestJ+1], bestText))
			i = bestJ + 1
			continue
		}
		out = append(out, words[i])
		i++
	}
	return out
}

func glueable(w Word) bool {
	t := strings.TrimSpace(w.Text)
	if t == "" {
		return false
	}
	if t == "." || t == "," || isJustCurrency(t) {
		return true
	}
	_, ok := normalizePriceToken(t)
	return ok
}

func spanAcceptable(parts []Word, mode amountMode) (string, bool) {
	text := joinWordText(parts)
	if _, ok := parseDecimalPrice(text); ok {
		return text, true
	}
	if mode == modeInteger {
		for _, p := range parts {
			if _, ok := parseDecimalPrice(p.Text); ok {
				return "", false
			}
		}
		if _, ok := parseIntegerPrice(text); ok {
			return text, true
		}
		return "", false
	}
	if _, ok := parseWholeMajor(text); ok {
		return text, true
	}
	return "", false
}

func joinWordText(words []Word) string {
	var b strings.Builder
	for _, w := range words {
		b.WriteString(strings.TrimSpace(w.Text))
	}
	return b.String()
}

func mergedWord(words []Word, text string) Word {
	w := words[0]
	w.Text = text
	last := words[len(words)-1]
	top, bottom := w.Top, w.Bottom()
	conf := w.Conf
	for _, p := range words[1:] {
		if p.Top < top {
			top = p.Top
		}
		if p.Bottom() > bottom {
			bottom = p.Bottom()
		}
		if p.Conf > 0 && (conf == 0 || p.Conf < conf) {
			conf = p.Conf
		}
	}
	w.Top = top
	w.Height = bottom - top
	if w.Height < 1 {
		w.Height = 1
	}
	w.Width = last.Right() - w.Left
	if w.Width < 1 {
		w.Width = 1
	}
	w.Conf = conf
	return w
}

func imageWidth(words []Word) int {
	max := 0
	for _, w := range words {
		if r := w.Right(); r > max {
			max = r
		}
	}
	return max
}

func confidenceScore(items []store.Item, words []Word, candidates int, columnFound bool, tightness float64) float64 {
	if len(words) == 0 {
		return 0
	}
	var sum float64
	for _, w := range words {
		c := w.Conf
		if c < 0 {
			c = 0
		}
		if c > 100 {
			c = 100
		}
		sum += float64(c)
	}
	avg := sum / float64(len(words)) / 100
	match := 0.0
	if candidates > 0 {
		match = float64(len(items)) / float64(candidates)
		if match > 1 {
			match = 1
		}
	}
	score := 0.5*avg + 0.35*match + 0.15*tightness
	if !columnFound {
		score *= 0.45
	}
	if len(items) == 0 {
		score *= 0.25
	}
	if score > 1 {
		score = 1
	}
	if score < 0 {
		score = 0
	}
	return score
}

func medianLineHeight(lines []Line) int {
	hs := make([]int, 0, len(lines))
	for _, l := range lines {
		h := l.Bottom - l.Top
		if h > 0 {
			hs = append(hs, h)
		}
	}
	return medianInt(hs)
}

func medianWordHeight(lines []Line) int {
	var hs []int
	for _, l := range lines {
		for _, w := range l.Words {
			if w.Height > 0 {
				hs = append(hs, w.Height)
			}
		}
	}
	m := medianInt(hs)
	if m <= 0 {
		return 16
	}
	return m
}

func medianInt(vs []int) int {
	if len(vs) == 0 {
		return 0
	}
	cp := append([]int(nil), vs...)
	sort.Ints(cp)
	return cp[len(cp)/2]
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
