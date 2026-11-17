package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"satsplit/internal/httpapi"
	"satsplit/internal/ocr"
	"satsplit/internal/rates"
	"satsplit/internal/store/sqlite"
	"satsplit/internal/translate"
	"satsplit/internal/vision"
)

func main() {
	addr := envOrDefault("SAT_SPLIT_ADDR", ":8080")

	ocrService := ocr.NewTesseractOCR(ocr.TesseractConfig{
		Command: envOrDefault("TESSERACT_CMD", "tesseract"),
		Timeout: 20 * time.Second,
		Lang:    envOrDefault("TESSERACT_LANG", "eng"),
		PSM:     int(envInt64("TESSERACT_PSM", 6)),
		OEM:     int(envInt64("TESSERACT_OEM", 3)),
	})

	dbPath := envOrDefault("SAT_SPLIT_DB", "satsplit.db")
	submissionStore, err := sqlite.Open(dbPath)
	if err != nil {
		log.Fatalf("db open error: %v", err)
	}
	defer func() {
		if err := submissionStore.Close(); err != nil {
			log.Printf("db close error: %v", err)
		}
	}()

	var translator translate.Service
	if url := envOrDefault("TRANSLATE_URL", ""); url != "" {
		translator = translate.NewClient(url)
	}

	var visionSvc vision.Service
	visionURL := envOrDefault("VISION_URL", "")
	if visionURL == "" {
		visionURL = envOrDefault("LLAMA_URL", "")
	}
	if visionURL != "" {
		primary := vision.Config{
			BaseURL: visionURL,
			APIKey:  envOrDefault("VISION_API_KEY", ""),
			Model:   envOrDefault("VISION_MODEL", ""),
		}
		visionSvc = vision.NewClient(primary)
		// VISION_FALLBACK_MODEL is a stronger model on the same endpoint. It is
		// asked only when the first model's items do not add up to the printed
		// total. Each model then gets two rounds.
		if fallbackModel := envOrDefault("VISION_FALLBACK_MODEL", ""); fallbackModel != "" && fallbackModel != primary.Model {
			primary.MaxAttempts = 2
			secondary := primary
			secondary.Model = fallbackModel
			visionSvc = vision.WithFallback(vision.NewClient(primary), vision.NewClient(secondary))
			log.Printf("vision model %q, falling back to %q when the items do not add up", primary.Model, fallbackModel)
		}
	}
	visionOCRHint := false
	if visionSvc != nil {
		switch strings.ToLower(envOrDefault("VISION_OCR_HINT", "")) {
		case "1", "true", "yes", "on":
			visionOCRHint = true
		}
		log.Printf("vision enabled at %s; it is the only reader of the item list (Tesseract text as a hint: %v)", visionURL, visionOCRHint)
	}

	server := httpapi.NewServer(httpapi.Config{
		OCR:             ocrService,
		Vision:          visionSvc,
		Store:           submissionStore,
		BaseURL:         envOrDefault("SAT_SPLIT_BASE_URL", ""),
		FiatCentsPerSat: envInt64("SAT_SPLIT_CENTS_PER_SAT", 1),
		UploadDir:       envOrDefault("SAT_SPLIT_UPLOAD_DIR", "uploads"),
		DefaultOCRLang:  envOrDefault("TESSERACT_LANG", "eng"),
		Rates:           rates.NewCoinbase(),
		Translator:      translator,
		VisionOCRHint:   visionOCRHint,
	})

	log.Printf("satsplit listening on %s", addr)
	if err := http.ListenAndServe(addr, server.Routes()); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
