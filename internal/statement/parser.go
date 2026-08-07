// Package statement extracts text from bank-statement PDFs. The Parser
// interface decouples the service from any specific extraction engine; the
// active provider is chosen by config (statement.parser), mirroring the OCR
// provider selector under registration.ocr.provider.
//
// Only the PDF's text layer is read here. Scanned/image-only statements carry
// no text layer and surface ErrUnparseable; a Vision-OCR Parser implementation
// can be added later without changing callers, since they depend only on this
// interface.
package statement

import "errors"

// ErrUnparseable is returned when a PDF carries no usable text layer — almost
// always a scanned statement rendered as images. Callers should treat this as a
// user-facing "could not read this statement" failure rather than a server bug.
var ErrUnparseable = errors.New("statement PDF has no extractable text layer (it may be a scanned image)")

// minTextChars is the threshold below which extracted text is treated as
// absent. Real statements run to thousands of characters; a handful of
// boilerplate glyphs means the pages are images.
const minTextChars = 40

// Parser extracts the text of a bank-statement PDF.
type Parser interface {
	// Extract returns the concatenated text of every page and the page count.
	// It returns ErrUnparseable when the PDF has no usable text layer.
	Extract(pdfBytes []byte) (text string, numPages int, err error)
}
