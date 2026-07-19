package service

import (
	"errors"
	"testing"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/ocr"
)

func newTestValidator() *PanValidator {
	return NewPanValidator(config.PANConfig{NameMatchDistance: 2})
}

func TestPanValidate_HappyPath(t *testing.T) {
	v := newTestValidator()
	r := &ocr.Result{PANNumber: "ABCDE1234F", Name: "Sample User", Confidence: 0.9}
	if err := v.Validate("ABCDE1234F", "Sample", "User", r); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestPanValidate_BadFormat(t *testing.T) {
	v := newTestValidator()
	r := &ocr.Result{Confidence: 1.0}
	err := v.Validate("abc", "Aa", "Bb", r)
	if !isPanFailure(err) {
		t.Fatalf("expected PanFailure, got %v", err)
	}
}

func TestPanValidate_PANMismatch(t *testing.T) {
	v := newTestValidator()
	r := &ocr.Result{PANNumber: "ABCDE1234F", Name: "Sample User", Confidence: 0.9}
	err := v.Validate("XXXXX0000X", "Sample", "User", r)
	if !isPanFailure(err) {
		t.Fatalf("expected PanFailure, got %v", err)
	}
}

func TestPanValidate_NameFuzzyWithinThreshold(t *testing.T) {
	v := newTestValidator()
	// OCR introduces two char diffs ("Sampel User" vs "Sample User"). Distance 2.
	r := &ocr.Result{PANNumber: "ABCDE1234F", Name: "Sampel User", Confidence: 0.9}
	if err := v.Validate("ABCDE1234F", "Sample", "User", r); err != nil {
		t.Fatalf("expected accept within threshold, got %v", err)
	}
}

func TestPanValidate_NameBeyondThreshold(t *testing.T) {
	v := newTestValidator()
	r := &ocr.Result{PANNumber: "ABCDE1234F", Name: "Rahul Sharma", Confidence: 0.9}
	err := v.Validate("ABCDE1234F", "Sample", "User", r)
	if !isPanFailure(err) {
		t.Fatalf("expected PanFailure, got %v", err)
	}
}

func TestPanValidate_LowConfidence(t *testing.T) {
	v := newTestValidator()
	r := &ocr.Result{PANNumber: "ABCDE1234F", Name: "Sample User", Confidence: 0.3}
	err := v.Validate("ABCDE1234F", "Sample", "User", r)
	if !isPanFailure(err) {
		t.Fatalf("expected PanFailure, got %v", err)
	}
}

func TestPanValidate_MissingNames(t *testing.T) {
	v := newTestValidator()
	r := &ocr.Result{Confidence: 1.0}
	if err := v.Validate("ABCDE1234F", "", "User", r); !isPanFailure(err) {
		t.Fatalf("expected PanFailure for empty first, got %v", err)
	}
	if err := v.Validate("ABCDE1234F", "Sam", "", r); !isPanFailure(err) {
		t.Fatalf("expected PanFailure for empty last, got %v", err)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"kitten", "sitting", 3},
		{"sample user", "sampel user", 2},
		{"flaw", "lawn", 2},
	}
	for _, tc := range cases {
		got := levenshtein(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("levenshtein(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func isPanFailure(err error) bool {
	var pf *apperr.PanFailure
	return errors.As(err, &pf)
}
