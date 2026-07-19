package service

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/ocr"
)

var panFormat = regexp.MustCompile(`^[A-Z]{5}[0-9]{4}[A-Z]$`)

// PanValidator enforces PAN format and OCR consistency (PAN exact, name fuzzy).
// It does NOT verify PAN authenticity against the income-tax database.
type PanValidator struct {
	cfg config.PANConfig
}

func NewPanValidator(cfg config.PANConfig) *PanValidator {
	return &PanValidator{cfg: cfg}
}

// Validate throws an apperr on any failure; otherwise returns nil.
func (v *PanValidator) Validate(submittedPAN, firstName, lastName string, r *ocr.Result) error {
	if !panFormat.MatchString(strings.ToUpper(strings.TrimSpace(submittedPAN))) {
		return apperr.NewPanFailure("PAN must be 5 letters, 4 digits, 1 letter (e.g. ABCDE1234F)")
	}
	if strings.TrimSpace(firstName) == "" || strings.TrimSpace(lastName) == "" {
		return apperr.NewPanFailure("First name and last name are required")
	}
	if r == nil {
		return apperr.NewPanFailure("OCR could not read the PAN image")
	}
	if r.Confidence < v.cfg.MinConfidence {
		return apperr.NewPanFailure(fmt.Sprintf(
			"PAN image was not clear enough (confidence %.2f)", r.Confidence))
	}

	// PAN number must match exactly after normalization.
	submitted := strings.ToUpper(strings.TrimSpace(submittedPAN))
	ocrPAN := ""
	if r.PANNumber != "" {
		ocrPAN = strings.ToUpper(strings.TrimSpace(r.PANNumber))
	}
	if ocrPAN != submitted {
		return apperr.NewPanFailure("PAN number on the image does not match the entered value")
	}

	// Name match is fuzzy.
	if r.Name == "" {
		return apperr.NewPanFailure("Could not read name from the PAN image")
	}
	fullName := strings.TrimSpace(firstName + " " + lastName)
	a := normalizeName(fullName)
	b := normalizeName(r.Name)
	if levenshtein(a, b) > v.cfg.NameMatchDistance {
		return apperr.NewPanFailure("Name on the image does not match the entered name")
	}
	return nil
}

// normalizeName: lowercase, strip non-letter/non-space, collapse spaces.
func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	out := strings.Builder{}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsSpace(r) {
			out.WriteRune(r)
		}
	}
	// collapse runs of whitespace
	fields := strings.Fields(out.String())
	return strings.Join(fields, " ")
}

// levenshtein is the classic O(m*n) DP edit distance.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			min := del
			if ins < min {
				min = ins
			}
			if sub < min {
				min = sub
			}
			curr[j] = min
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
