package statement

import (
	"bytes"
	"strings"

	"github.com/ledongthuc/pdf"
)

// PDFParser is the production Parser backed by github.com/ledongthuc/pdf, a
// pure-Go reader that pulls text from the PDF's text layer (no CGO, no system
// deps). It works on digitally-generated statements (HDFC/ICICI/SBI/Axis
// net-banking PDFs) but cannot read scanned/image-only statements — those carry
// no text layer and Extract returns ErrUnparseable so the caller can surface a
// clear "re-export as text" message rather than an empty analysis.
type PDFParser struct{}

func NewPDFParser() *PDFParser { return &PDFParser{} }

// Extract reads the PDF's plain text and reports its page count. The whole PDF
// is read from the in-memory byte slice via a bytes.Reader, so no temp files
// are written. Encrypted/corrupt PDFs surface as ErrUnparseable.
func (p *PDFParser) Extract(pdfBytes []byte) (string, int, error) {
	if len(pdfBytes) == 0 {
		return "", 0, ErrUnparseable
	}
	// bytes.Reader implements io.ReaderAt, which NewReader requires.
	r, err := pdf.NewReader(bytes.NewReader(pdfBytes), int64(len(pdfBytes)))
	if err != nil {
		// A corrupt or password-protected PDF isn't "unparseable text" in the
		// scanned-image sense, but the caller's remedy is the same: ask the
		// user for a different file. Treat both as ErrUnparseable.
		return "", 0, ErrUnparseable
	}

	plain, err := r.GetPlainText()
	if err != nil {
		return "", 0, ErrUnparseable
	}
	var buf bytes.Buffer
	// GetPlainText returns an io.Reader of the decoded content stream; read it
	// to completion. A read error here likewise means no usable text.
	if _, err := buf.ReadFrom(plain); err != nil {
		return "", 0, ErrUnparseable
	}

	text := strings.TrimSpace(buf.String())
	// Below the threshold the pages are almost certainly images (scanned
	// statements). Treat as unparseable so the row is marked failed with the
	// clear scanned-PDF message rather than an empty analysis.
	if len(text) < minTextChars {
		return "", r.NumPage(), ErrUnparseable
	}
	return text, r.NumPage(), nil
}
