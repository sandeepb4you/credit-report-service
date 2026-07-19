package ocr

// Stub is a dev-only provider that returns a deterministic mock result so the
// flow runs end-to-end without any cloud credentials.
//
// Mirrors the Spring StubOcrClient: PAN "ABCDE1234F", name "SAMPLE USER".
type Stub struct{}

func NewStub() *Stub { return &Stub{} }

func (s *Stub) Extract(imageBytes []byte, contentType string) (*Result, error) {
	text := "INCOME TAX DEPARTMENT\nNAME: SAMPLE USER\nABCDE1234F"
	return &Result{
		Text:       text,
		PANNumber:  ExtractPAN(text),
		Name:       ExtractName(text),
		Confidence: 1.0,
	}, nil
}
