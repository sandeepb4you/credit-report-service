// Package ocr extracts text from PAN-card images. The Provider interface
// decouples the service from any specific OCR engine; the active provider is
// chosen by config (registration.ocr.provider).
package ocr

// Result of OCR on a PAN-card image. Confidence is provider-reported when
// available (0..1), otherwise best-effort.
type Result struct {
	Text       string
	PANNumber  string
	Name       string
	Confidence float64
}

// Provider extracts text and a best-effort PAN + holder name from image bytes.
type Provider interface {
	Extract(imageBytes []byte, contentType string) (*Result, error)
}
