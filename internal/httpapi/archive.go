package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"satsplit/internal/store"
	"satsplit/internal/vision"
)

// archiveDir is where every uploaded receipt photo is kept for later checks.
// The files are not deleted when a receipt is edited or when OCR fails.
func (s *Server) archiveDir() string {
	return filepath.Join(s.uploadDir, "archive")
}

type archiveRecord struct {
	Token     string        `json:"token"`
	Currency  string        `json:"currency"`
	Lang      string        `json:"lang,omitempty"`
	SavedAt   time.Time     `json:"saved_at"`
	Image     string        `json:"image"`
	Tesseract *archiveParse `json:"tesseract,omitempty"`
	Vision    *archiveParse `json:"vision,omitempty"`
}

type archiveParse struct {
	Source     string       `json:"source"`
	Text       string       `json:"text,omitempty"`
	Items      []store.Item `json:"items,omitempty"`
	TaxCents   int64        `json:"tax_cents"`
	Confidence float64      `json:"confidence,omitempty"`
	Pass       string       `json:"pass,omitempty"`
	Error      string       `json:"error,omitempty"`

	// Vision only. Attempts holds every request and raw reply, including the
	// rounds where the items did not add up to the printed subtotal.
	Model         string           `json:"model,omitempty"`
	SubtotalCents int64            `json:"subtotal_cents,omitempty"`
	TotalCents    int64            `json:"total_cents,omitempty"`
	Verifiable    bool             `json:"verifiable,omitempty"`
	Balanced      bool             `json:"balanced,omitempty"`
	DiffCents     int64            `json:"diff_cents,omitempty"`
	Attempts      []vision.Attempt `json:"attempts,omitempty"`
}

// keepReceiptImage copies the uploaded photo into the archive and writes a
// sidecar the parser results can be added to. A copy failure is returned to
// the caller, which should log it and still accept the receipt.
func (s *Server) keepReceiptImage(token, srcPath, currency, lang string) (string, error) {
	if token == "" || srcPath == "" {
		return "", fmt.Errorf("archive: missing token or path")
	}
	dir := s.archiveDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(srcPath))
	if ext == "" {
		ext = ".jpg"
	}
	name := token + ext
	dest := filepath.Join(dir, name)
	if err := copyFile(srcPath, dest); err != nil {
		return "", err
	}
	rec := archiveRecord{
		Token:    token,
		Currency: currency,
		Lang:     lang,
		SavedAt:  time.Now().UTC(),
		Image:    name,
	}
	if err := s.writeArchive(token, rec); err != nil {
		return dest, err
	}
	if err := s.appendArchiveIndex(rec); err != nil {
		return dest, err
	}
	return dest, nil
}

func (s *Server) recordArchiveParse(token, source, text, pass string, items []store.Item, tax int64, confidence float64, parseErr error) {
	if token == "" {
		return
	}
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()

	rec, err := s.readArchive(token)
	if err != nil {
		return
	}
	parsed := &archiveParse{
		Source:     source,
		Text:       text,
		Items:      items,
		TaxCents:   tax,
		Confidence: confidence,
		Pass:       pass,
	}
	if parseErr != nil {
		parsed.Error = parseErr.Error()
	}
	switch source {
	case "vision":
		rec.Vision = parsed
	default:
		rec.Tesseract = parsed
	}
	_ = s.writeArchiveUnlocked(token, rec)
}

// recordArchiveVision stores a vision pass with every attempt it made.
func (s *Server) recordArchiveVision(token string, ext vision.Extraction, parseErr error) {
	if token == "" {
		return
	}
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()

	rec, err := s.readArchive(token)
	if err != nil {
		return
	}
	parsed := &archiveParse{
		Source:        "vision",
		Items:         ext.Items,
		TaxCents:      ext.TaxCents,
		Model:         ext.Model,
		SubtotalCents: ext.SubtotalCents,
		TotalCents:    ext.TotalCents,
		Verifiable:    ext.Verifiable,
		Balanced:      ext.Balanced,
		DiffCents:     ext.DiffCents,
		Attempts:      ext.Attempts,
	}
	if parseErr != nil {
		parsed.Error = parseErr.Error()
	}
	rec.Vision = parsed
	_ = s.writeArchiveUnlocked(token, rec)
}

func (s *Server) writeArchive(token string, rec archiveRecord) error {
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()
	return s.writeArchiveUnlocked(token, rec)
}

func (s *Server) writeArchiveUnlocked(token string, rec archiveRecord) error {
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return os.WriteFile(filepath.Join(s.archiveDir(), token+".json"), body, 0o644)
}

func (s *Server) readArchive(token string) (archiveRecord, error) {
	body, err := os.ReadFile(filepath.Join(s.archiveDir(), token+".json"))
	if err != nil {
		return archiveRecord{}, err
	}
	var rec archiveRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return archiveRecord{}, err
	}
	return rec, nil
}

func (s *Server) appendArchiveIndex(rec archiveRecord) error {
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.archiveDir(), "index.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(struct {
		Token    string    `json:"token"`
		Image    string    `json:"image"`
		Currency string    `json:"currency"`
		SavedAt  time.Time `json:"saved_at"`
	}{rec.Token, rec.Image, rec.Currency, rec.SavedAt})
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// receiptOCRLang picks the Tesseract language. Dominican receipts stay
// readable when Spanish and English data are both loaded.
func receiptOCRLang(requested, fallback, currency string) string {
	lang := strings.TrimSpace(requested)
	if lang == "" {
		lang = strings.TrimSpace(fallback)
	}
	if lang == "" {
		lang = "eng"
	}
	if strings.EqualFold(strings.TrimSpace(currency), "DOP") && (lang == "eng" || lang == "spa") {
		return "spa+eng"
	}
	return lang
}
