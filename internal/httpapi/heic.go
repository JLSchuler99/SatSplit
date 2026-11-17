package httpapi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// heifConvertCmd is the libheif tool that turns HEIC into JPEG. The container
// image installs it from the libheif-examples package.
const heifConvertCmd = "heif-convert"

// isHEIC reports whether the file is an HEIF container holding an HEVC image,
// the default iPhone photo format. It checks the content, not the file name.
func isHEIC(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 12)
	if _, err := f.Read(head); err != nil {
		return false
	}
	if string(head[4:8]) != "ftyp" {
		return false
	}
	switch string(head[8:12]) {
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
		return true
	}
	return false
}

// convertHEICToJPEG writes a JPEG next to the HEIC file, removes the original
// and returns the new path. Browsers, Tesseract and hosted vision models do
// not read HEIC, so the JPEG is what the rest of the app works with.
func convertHEICToJPEG(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	base := strings.TrimSuffix(path, filepath.Ext(path))
	tmp := base + ".converted.jpg"
	out, err := exec.CommandContext(ctx, heifConvertCmd, "-q", "92", path, tmp).CombinedOutput()
	if err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("heif-convert: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if _, err := os.Stat(tmp); err != nil {
		return "", fmt.Errorf("heif-convert wrote no file: %w", err)
	}
	final := base + ".jpg"
	if err := os.Remove(path); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", err
	}
	return final, nil
}
