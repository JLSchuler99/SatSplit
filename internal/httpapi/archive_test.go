package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"satsplit/internal/store"
)

func TestKeepReceiptImageWritesCopyAndSidecar(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "shot.jpg")
	if err := os.WriteFile(src, []byte("jpeg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{uploadDir: dir}
	archived, err := s.keepReceiptImage("abc123", src, "DOP", "spa+eng")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(archived)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "jpeg-bytes" {
		t.Fatalf("image = %q", got)
	}
	s.recordArchiveParse("abc123", "tesseract", "Mofongo 500", "psm6", []store.Item{{
		Label: "Mofongo", AmountCents: 50000, SplitCount: 1,
	}}, 14000, 0.4, nil)

	body, err := os.ReadFile(filepath.Join(dir, "archive", "abc123.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, part := range []string{`"token": "abc123"`, `"image": "abc123.jpg"`, `"Mofongo"`, `"tax_cents": 14000`} {
		if !strings.Contains(text, part) {
			t.Fatalf("sidecar missing %s in %s", part, text)
		}
	}
	index, err := os.ReadFile(filepath.Join(dir, "archive", "index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `"token":"abc123"`) || !strings.Contains(string(index), `"image":"abc123.jpg"`) {
		t.Fatalf("index = %s", index)
	}
}

func TestReceiptOCRLangDominican(t *testing.T) {
	if got := receiptOCRLang("", "eng", "DOP"); got != "spa+eng" {
		t.Fatalf("default DOP = %s", got)
	}
	if got := receiptOCRLang("spa", "eng", "DOP"); got != "spa+eng" {
		t.Fatalf("spa DOP = %s", got)
	}
	if got := receiptOCRLang("jpn", "eng", "DOP"); got != "jpn" {
		t.Fatalf("explicit = %s", got)
	}
	if got := receiptOCRLang("", "eng", "USD"); got != "eng" {
		t.Fatalf("usd = %s", got)
	}
}
