package server

import (
	"fmt"
	"strconv"
	"strings"
)

// bodyLimitBytes parses a human size string ("5MB", "10 MiB", "10240") into
// bytes. Falls back to a 10 MiB default on parse error.
func bodyLimitBytes(s string) int {
	if n, ok := parseSize(s); ok {
		return n
	}
	return 10 * 1024 * 1024
}

func parseSize(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// All-numeric -> bytes.
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	// Walk the string collecting the numeric prefix.
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	numPart := s[:i]
	unitPart := strings.ToLower(strings.TrimSpace(s[i:]))
	f, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, false
	}
	mult := 1.0
	switch unitPart {
	case "", "b":
		mult = 1
	case "kb":
		mult = 1000
	case "kib":
		mult = 1024
	case "mb":
		mult = 1000 * 1000
	case "mib":
		mult = 1024 * 1024
	case "gb":
		mult = 1000 * 1000 * 1000
	case "gib":
		mult = 1024 * 1024 * 1024
	case "k":
		mult = 1024
	case "m":
		mult = 1024 * 1024
	case "g":
		mult = 1024 * 1024 * 1024
	default:
		return 0, false
	}
	_ = fmt.Sprintf
	return int(f * mult), true
}
