package ocr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TesseractConfig struct {
	Command string
	Timeout time.Duration
	Lang    string
	PSM     int
	OEM     int
}

type TesseractOCR struct {
	command string
	timeout time.Duration
	lang    string
	psm     int
	oem     int

	mu      sync.Mutex
	skipDPI bool
}

func NewTesseractOCR(cfg TesseractConfig) *TesseractOCR {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	return &TesseractOCR{
		command: cfg.Command,
		timeout: timeout,
		lang:    cfg.Lang,
		psm:     cfg.PSM,
		oem:     cfg.OEM,
	}
}

type Request struct {
	Lang     string
	PSM      int
	OEM      int
	Currency string
}

// Extract normalizes the photo, then runs Tesseract. A weak first pass is
// retried after orientation correction and with the other single-column page
// mode. The stronger parse is kept, so a bad retry cannot replace a good one.
func (t *TesseractOCR) Extract(ctx context.Context, imagePath string, req Request) (Result, error) {
	if t.command == "" {
		return Result{}, errors.New("tesseract command not configured")
	}

	path := imagePath
	cleanup := func() {}
	prepared, prepCleanup, err := PrepareImage(imagePath, PrepOptions{
		MinLongEdge: 1600,
		MaxLongEdge: 2200,
	})
	if err != nil {
		log.Printf("ocr: preprocess failed, using original image: %v", err)
	} else {
		path = prepared
		cleanup = prepCleanup
	}
	defer cleanup()

	primary := req.PSM
	if primary == 0 {
		primary = t.psm
	}
	if primary <= 0 {
		primary = 6
	}
	alt := 4
	if primary == 4 {
		alt = 6
	}

	res, err := t.pass(ctx, path, req, primary)
	if err != nil {
		if !timeLeft(ctx, 3*time.Second) {
			return Result{}, err
		}
		altRes, altErr := t.pass(ctx, path, req, alt)
		if altErr != nil {
			return t.withEnhance(ctx, imagePath, req, primary, Result{}, err)
		}
		altRes.Stats.Pass = fmt.Sprintf("psm%d", alt)
		return t.withEnhance(ctx, imagePath, req, primary, altRes, nil)
	}
	res.Stats.Pass = fmt.Sprintf("psm%d", primary)
	if res.strongEnough() || !timeLeft(ctx, 3*time.Second) {
		return res, nil
	}

	if degrees, conf, osdErr := t.detectRotation(ctx, path); osdErr == nil && conf >= 5 && (degrees == 90 || degrees == 180 || degrees == 270) {
		rotated, rotCleanup, rotErr := RotateImageFile(path, degrees)
		if rotErr == nil {
			defer rotCleanup()
			rotatedRes, rotOCRErr := t.pass(ctx, rotated, req, primary)
			if rotOCRErr == nil {
				rotatedRes.Stats.Pass = fmt.Sprintf("psm%d-rot%d", primary, degrees)
				chosen := betterResult(res, rotatedRes)
				if chosen.Stats.Pass == rotatedRes.Stats.Pass {
					path = rotated
				}
				res = chosen
			}
		}
	}
	if res.strongEnough() || !timeLeft(ctx, 3*time.Second) {
		return res, nil
	}

	altRes, altErr := t.pass(ctx, path, req, alt)
	if altErr != nil {
		return t.withEnhance(ctx, imagePath, req, primary, res, nil)
	}
	altRes.Stats.Pass = fmt.Sprintf("psm%d", alt)
	return t.withEnhance(ctx, imagePath, req, primary, betterResult(res, altRes), nil)
}

// withEnhance retries a weak parse on a contrast-stretched copy. The plain
// photo stays the default: sharpening thermal receipts often makes them worse.
func (t *TesseractOCR) withEnhance(ctx context.Context, imagePath string, req Request, primary int, res Result, failure error) (Result, error) {
	empty := res.Text == "" && len(res.Items) == 0
	if res.strongEnough() || !timeLeft(ctx, 3*time.Second) {
		if failure != nil && empty {
			return Result{}, failure
		}
		return res, nil
	}
	enhanced, cleanup, err := PrepareImage(imagePath, PrepOptions{
		MinLongEdge: 1600,
		MaxLongEdge: 2200,
		Enhance:     true,
	})
	if err != nil {
		if failure != nil && empty {
			return Result{}, failure
		}
		return res, nil
	}
	defer cleanup()
	enh, enhErr := t.pass(ctx, enhanced, req, primary)
	if enhErr != nil {
		if failure != nil && empty {
			return Result{}, failure
		}
		return res, nil
	}
	enh.Stats.Pass = fmt.Sprintf("psm%d-enhance", primary)
	if failure != nil && empty {
		return enh, nil
	}
	return betterResult(res, enh), nil
}

func (t *TesseractOCR) pass(ctx context.Context, imagePath string, req Request, psm int) (Result, error) {
	if !timeLeft(ctx, time.Second) {
		return Result{}, context.DeadlineExceeded
	}
	lang := req.Lang
	if lang == "" {
		lang = t.lang
	}
	stdout, err := t.runTSV(ctx, imagePath, lang, psm, req.OEM)
	if err != nil && lang != "eng" && langMissing(err) {
		log.Printf("ocr: language %q unavailable, retrying eng: %v", lang, err)
		stdout, err = t.runTSV(ctx, imagePath, "eng", psm, req.OEM)
	}
	if err != nil {
		return Result{}, err
	}
	res := ExtractFromTSVCurrency(stdout, req.Currency)
	return res, nil
}

func (t *TesseractOCR) runTSV(ctx context.Context, imagePath, lang string, psm, oem int) (string, error) {
	ctx, cancel := t.withPassTimeout(ctx)
	defer cancel()

	t.mu.Lock()
	skipDPI := t.skipDPI
	t.mu.Unlock()

	stdout, err := t.exec(ctx, imagePath, lang, psm, oem, !skipDPI)
	if err != nil && !skipDPI && dpiRejected(err) {
		t.mu.Lock()
		t.skipDPI = true
		t.mu.Unlock()
		stdout, err = t.exec(ctx, imagePath, lang, psm, oem, false)
	}
	return stdout, err
}

func (t *TesseractOCR) exec(ctx context.Context, imagePath, lang string, psm, oem int, dpi bool) (string, error) {
	args := []string{imagePath, "stdout"}
	if lang != "" {
		args = append(args, "-l", lang)
	}
	if psm > 0 {
		args = append(args, "--psm", strconv.Itoa(psm))
	}
	if oem == 0 {
		oem = t.oem
	}
	if oem > 0 {
		args = append(args, "--oem", strconv.Itoa(oem))
	}
	if dpi {
		args = append(args, "--dpi", "300")
	}
	args = append(args, "tsv")

	cmd := exec.CommandContext(ctx, t.command, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("tesseract: %w: %s", err, truncate(msg, 400))
		}
		return "", err
	}
	return stdout.String(), nil
}

func (t *TesseractOCR) detectRotation(ctx context.Context, imagePath string) (degrees int, confidence float64, err error) {
	if !timeLeft(ctx, 2*time.Second) {
		return 0, 0, context.DeadlineExceeded
	}
	ctx, cancel := t.withPassTimeout(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.command, imagePath, "stdout", "--psm", "0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return 0, 0, fmt.Errorf("tesseract osd: %w: %s", err, truncate(msg, 200))
		}
		return 0, 0, err
	}
	return parseOSD(stdout.String() + "\n" + stderr.String())
}

func parseOSD(text string) (degrees int, confidence float64, err error) {
	degrees = -1
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Rotate:") {
			n, convErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Rotate:")))
			if convErr == nil {
				degrees = n
			}
		}
		if strings.HasPrefix(line, "Orientation confidence:") {
			f, convErr := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "Orientation confidence:")), 64)
			if convErr == nil {
				confidence = f
			}
		}
	}
	if degrees < 0 {
		return 0, confidence, errors.New("tesseract osd: no rotation")
	}
	return degrees, confidence, nil
}

func (t *TesseractOCR) withPassTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := 12 * time.Second
	if t.timeout > 0 && t.timeout < budget {
		budget = t.timeout
	}
	if deadline, ok := ctx.Deadline(); ok {
		remain := time.Until(deadline)
		if remain < budget {
			budget = remain
		}
	}
	if budget < 0 {
		budget = 0
	}
	return context.WithTimeout(ctx, budget)
}

func betterResult(a, b Result) Result {
	if resultScore(b) > resultScore(a)+0.02 {
		return b
	}
	return a
}

func resultScore(r Result) float64 {
	n := float64(len(r.Items))
	if n > 12 {
		n = 12
	}
	return r.Confidence + n*0.01
}

func timeLeft(ctx context.Context, need time.Duration) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(deadline) > need
}

func langMissing(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "traineddata") ||
		strings.Contains(msg, "failed loading language") ||
		strings.Contains(msg, "could not initialize")
}

func dpiRejected(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "dpi")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
