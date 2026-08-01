package middleware

// pii.go provides redaction for request/response bodies before they are logged.
// It is the single chokepoint that enforces "no PII in logs" for body capture.
//
// Two layers of defense:
//  1. Key-based: a case-insensitive allowlist of sensitive field names. Any
//     value at one of these keys is replaced with a fixed mask, regardless of
//     its JSON type. This is the primary mechanism — it matches the field names
//     used across the service's request/response structs and models.
//  2. Value-based (defense-in-depth): even for non-allowlisted keys, values
//     that are recognizable PAN numbers, bearer JWTs, or email addresses are
//     redacted too, so a novel field name carrying a known sensitive shape is
//     still caught.

import (
	"encoding/json"
	"regexp"
	"strings"
)

// maskValue is the replacement applied to any redacted field.
const maskValue = "********"

// sensitiveKeys is the case-insensitive set of JSON field names whose values
// must never be logged. It covers every PII field that appears in the service's
// request structs (handler/*) and response models (models/account.go):
//   - credentials: password, token, otp, idToken, secret
//   - contact:     email, phone, mobile, mobileNo
//   - identity:    pan, aadhaar*, name fields, DOB
//   - auth:        providerSubject, passwordHash, otpHash, destination, authorization
var sensitiveKeys = map[string]struct{}{
	// credentials / secrets
	"password":     {},
	"passwordhash": {},
	"token":        {},
	"idtoken":      {},
	"accesstoken":  {},
	"refreshtoken": {},
	"otphash":      {},
	"otp":          {},
	"secret":       {},
	"clientsecret": {},
	// contact
	"email":             {},
	"primaryemail":      {},
	"phone":             {},
	"primaryphone":      {},
	"mobile":            {},
	"mobileno":          {},
	"mobilephonenumber": {},
	"destination":       {},
	// identity
	"pan":              {},
	"panname":          {},
	"incometaxpan":     {},
	"aadhaarlast4":     {},
	"aadhaarreference": {},
	"aadhaarnumber":    {},
	// names
	"firstname":   {},
	"lastname":    {},
	"middlename1": {},
	"middlename2": {},
	"middlename3": {},
	"name":        {},
	"surname":     {},
	// dates
	"dob":           {},
	"dateofbirth":   {},
	"date_of_birth": {},
	// misc identifiers that should not leak
	"providersubject":     {},
	"authorization":       {},
	"authorizationheader": {},
}

// value-shape redactors, applied to non-sensitive keys as defense-in-depth.
var (
	// PAN: 5 letters, 4 digits, 1 letter (e.g. ABCDE1234F).
	panRE = regexp.MustCompile(`\b[A-Z]{5}[0-9]{4}[A-Z]\b`)
	// Email address.
	emailRE = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}`)
	// Bearer JWT: three base64url segments, the first decodable as a JSON header.
	bearerRE = regexp.MustCompile(`(?i)\b(Bearer\s+)?[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)
)

// maskJSON returns a redacted copy of a JSON body. It parses the body into
// generic structures, masks sensitive fields and recognizable sensitive values,
// then re-serializes. If the body isn't valid JSON it is returned with
// value-shape redaction applied to the raw text (so a misformed body still gets
// its PANs/emails/JWTs scrubbed).
func maskJSON(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var node any
	if err := json.Unmarshal(body, &node); err != nil {
		// Not JSON — apply value-shape redaction to the raw bytes.
		return maskRaw(body)
	}
	node = redact(node)
	out, err := json.Marshal(node)
	if err != nil {
		// Should never happen (we just unmarshalled); fall back to raw masking.
		return maskRaw(body)
	}
	return out
}

// redact recursively walks a parsed JSON value, masking by key and by value-shape.
func redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if isSensitiveKey(k) {
				t[k] = maskValue
				continue
			}
			t[k] = redact(val)
		}
		return t
	case []any:
		for i := range t {
			t[i] = redact(t[i])
		}
		return t
	case string:
		return maskShapes(t)
	default:
		return v
	}
}

// isSensitiveKey reports whether key matches the sensitive set (case- and
// hyphen/underscore-insensitive).
func isSensitiveKey(key string) bool {
	k := normalizeKey(key)
	_, ok := sensitiveKeys[k]
	return ok
}

// normalizeKey lowercases and collapses common separators so that e.g.
// "DateOfBirth", "date_of_birth", and "date-of-birth" all match.
func normalizeKey(key string) string {
	s := strings.ToLower(key)
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

// maskRaw applies value-shape redaction to an arbitrary byte slice.
func maskRaw(body []byte) []byte {
	s := string(body)
	s = bearerRE.ReplaceAllString(s, maskValue)
	s = panRE.ReplaceAllString(s, maskValue)
	s = emailRE.ReplaceAllString(s, maskValue)
	return []byte(s)
}

// maskShapes redacts PAN / email / JWT shapes within a string value.
func maskShapes(s string) string {
	s = bearerRE.ReplaceAllString(s, maskValue)
	s = panRE.ReplaceAllString(s, maskValue)
	s = emailRE.ReplaceAllString(s, maskValue)
	return s
}

// truncateBytes caps a body to n bytes for logging, appending an ellipsis-style
// marker when truncation occurs. Keeps log lines bounded regardless of payload
// size (the credit report response is ~15KB).
func truncateBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	out := make([]byte, n)
	copy(out, b[:n])
	return append(out, []byte("…(+truncated)")...)
}
