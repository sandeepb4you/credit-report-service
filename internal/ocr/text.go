package ocr

import (
	"regexp"
	"strings"
)

// panTokenRegex matches a standard Indian PAN: 5 letters, 4 digits, 1 letter.
var panTokenRegex = regexp.MustCompile(`\b([A-Z]{5}[0-9]{4}[A-Z])\b`)

// nameMarkerRegex matches a line that announces the holder's name, e.g.
// "NAME", "NAME:", "/NAME/", or the Bengali "নাম". Case-insensitive.
var nameMarkerRegex = regexp.MustCompile(`(?i).*(?:^|\s)(?:name|নাম)(?:\s|/|:).*`)

// ExtractPAN returns the first PAN-like token in the OCR text, uppercased,
// or "" if none.
func ExtractPAN(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	m := panTokenRegex.FindStringSubmatch(strings.ToUpper(text))
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// ExtractName returns the holder name printed on the PAN card. It first looks
// for a line containing a "NAME" marker and takes the next non-empty line if
// it looks like a person's name; otherwise it falls back to the longest
// line that looks like a person's name.
func ExtractName(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	// First pass: marker line -> next plausible line.
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if nameMarkerRegex.MatchString(line) && i+1 < len(lines) {
			candidate := strings.TrimSpace(lines[i+1])
			if looksLikePersonName(candidate) {
				return normalizeName(candidate)
			}
		}
	}
	// Fallback: longest plausible line.
	best := ""
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if looksLikePersonName(t) && len(t) > len(best) {
			best = t
		}
	}
	if best == "" {
		return ""
	}
	return normalizeName(best)
}

// looksLikePersonName requires at least two alphabetic tokens, each starting
// with a letter — the heuristic Spring's PanTextUtils used.
func looksLikePersonName(s string) bool {
	tokens := strings.Fields(s)
	ok := 0
	for _, tk := range tokens {
		cleaned := strings.Map(keepLetters, tk)
		if cleaned != "" && isLetter(rune(cleaned[0])) {
			ok++
		}
	}
	return ok >= 2
}

func keepLetters(r rune) rune {
	if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
		return r
	}
	return -1
}

func isLetter(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func normalizeName(s string) string {
	// trim, collapse internal whitespace, strip trailing punctuation
	s = strings.TrimSpace(s)
	s = collapseSpaces(s)
	s = strings.TrimRight(s, ".,;:")
	return s
}

func collapseSpaces(s string) string {
	out := strings.Builder{}
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				out.WriteRune(' ')
			}
			prevSpace = true
		} else {
			out.WriteRune(r)
			prevSpace = false
		}
	}
	return out.String()
}
