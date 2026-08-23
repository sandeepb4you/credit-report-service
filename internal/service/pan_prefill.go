package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"credit-report-service/internal/config"
	"credit-report-service/internal/digitap"
)

// PAN verification against Digitap's Mobile to Prefill API.
//
// The question this answers is narrow: does the PAN and name the user typed
// belong to the mobile number they just proved control of over SMS OTP? It is
// NOT an income-tax-department check of whether the PAN exists — Digitap
// reports what is registered against the number, and a match means the two
// agree, nothing stronger.

// PrefillVerdict is one verification attempt's outcome.
type PrefillVerdict struct {
	// Verified is true only when the provider returned a record and both the
	// PAN and the name matched it.
	Verified bool
	// ProviderGap is true when the provider had nothing to compare against —
	// no record for the number (102), no name against it (103), or the data
	// source itself failed (503). Distinct from a mismatch: the user may well
	// have entered correct details that the provider simply cannot see.
	ProviderGap bool
	// ProviderName is the name the provider holds, stored for support and for
	// the admin console. Empty on a gap.
	ProviderName string
	// ProviderRef is Digitap's request_id — the handle their support needs to
	// trace a specific lookup. Not PII.
	ProviderRef string
	// Reason is a user-facing explanation when Verified is false.
	Reason string

	// The remainder is for the audit row in prefill_lookups, not for the user.

	// ClientRef is the reference we sent, so our row and Digitap's log line up.
	ClientRef string
	// ResultCode / Message are the provider's own verdict, kept verbatim: the
	// service's interpretation of them may change, theirs is the fact.
	ResultCode int
	Message    string
	// PANMatched / NameMatched are nil when no comparison happened (a gap or a
	// transport failure), which is a different thing from a comparison that
	// returned false.
	PANMatched  *bool
	NameMatched *bool
	// ResultJSON is the decoded subset of the provider's answer — deliberately
	// not the whole upstream body. See models.PrefillLookup.
	ResultJSON json.RawMessage
}

// PrefillVerifier compares submitted PAN details against the prefill API.
type PrefillVerifier struct {
	client *digitap.PrefillClient
	cfg    config.PANConfig
}

func NewPrefillVerifier(client *digitap.PrefillClient, cfg config.PANConfig) *PrefillVerifier {
	return &PrefillVerifier{client: client, cfg: cfg}
}

// IsStub reports whether verification is running against the offline stub, in
// which case a verified verdict proves nothing about the real person.
func (v *PrefillVerifier) IsStub() bool { return v.client.IsStub() }

// Verify looks the mobile number up and compares the result with what the user
// submitted. A transport or configuration failure returns an error; anything
// the user could act on comes back as an unverified verdict.
func (v *PrefillVerifier) Verify(ctx context.Context, accountID int64, mobile, pan, fullName string) (PrefillVerdict, error) {
	ref := clientRefFor(accountID)
	// Stamped on every return path, including the failures, so an audit row
	// always carries the reference we sent.
	fail := func(v PrefillVerdict) PrefillVerdict { v.ClientRef = ref; return v }

	out, err := v.client.Lookup(ctx, ref, mobile)
	if err != nil {
		switch {
		case errors.Is(err, digitap.ErrPrefillSource):
			// The spec says explicitly not to retry a source error, so treat it
			// as a gap rather than bouncing the user off a screen they cannot
			// get past by trying again.
			slog.Warn("pan prefill: upstream source error", "account_id", accountID, "client_ref", ref)
			return fail(PrefillVerdict{ProviderGap: true, Reason: "Could not reach the verification service"}), nil
		case errors.Is(err, digitap.ErrPrefillAuth), errors.Is(err, digitap.ErrPrefillServiceDisabled):
			// Misconfiguration on our side. Surface it loudly: silently
			// degrading to "unverified" would hide a broken deployment behind
			// what looks like ordinary user failure.
			slog.Error("pan prefill: provider rejected our credentials", "account_id", accountID, "error", err)
			return fail(PrefillVerdict{}), err
		default:
			return fail(PrefillVerdict{}), err
		}
	}

	verdict := PrefillVerdict{
		ProviderRef: out.RequestID,
		ClientRef:   ref,
		ResultCode:  out.ResultCode,
		Message:     out.Message,
	}
	if out.Result != nil {
		if b, mErr := json.Marshal(out.Result); mErr == nil {
			verdict.ResultJSON = b
		}
	}

	switch out.ResultCode {
	case digitap.PrefillFound:
		// fall through to comparison
	case digitap.PrefillNoRecord, digitap.PrefillNameMissing:
		slog.Info("pan prefill: no record to compare against",
			"account_id", accountID, "result_code", out.ResultCode, "client_ref", ref)
		verdict.ProviderGap = true
		verdict.Reason = "The verification service has no record for this mobile number"
		return verdict, nil
	default:
		verdict.ProviderGap = true
		verdict.Reason = fmt.Sprintf("Unexpected verification result (%d)", out.ResultCode)
		return verdict, nil
	}

	if out.Result == nil {
		verdict.ProviderGap = true
		verdict.Reason = "The verification service returned no details"
		return verdict, nil
	}

	providerPAN := strings.ToUpper(strings.TrimSpace(out.Result.BestPAN()))
	verdict.ProviderName = strings.TrimSpace(out.Result.Name)

	if providerPAN == "" {
		verdict.ProviderGap = true
		verdict.Reason = "The verification service holds no PAN for this mobile number"
		return verdict, nil
	}

	panOK := panMatches(pan, providerPAN)
	verdict.PANMatched = &panOK
	if !panOK {
		verdict.Reason = "That PAN is not the one registered against this mobile number"
		return verdict, nil
	}

	nameOK := nameMatches(fullName, verdict.ProviderName, v.cfg.NameMatchDistance)
	verdict.NameMatched = &nameOK
	if !nameOK {
		verdict.Reason = "That name does not match the name registered against this PAN"
		return verdict, nil
	}

	verdict.Verified = true
	return verdict, nil
}

// clientRefFor builds the per-request id Digitap echoes back and logs. The API
// constrains it to ^[a-zA-Z0-9 _-]*$ and 45 characters, so it carries only the
// account id, a timestamp and a nonce — never the mobile number or PAN, since
// it ends up in a third party's logs.
//
// The nonce is not decoration: the id must be unique per request, and a
// timestamp alone is not. Two retries a few milliseconds apart — or any two
// calls inside one clock tick, which on Windows can be several milliseconds —
// would otherwise submit the same reference twice.
func clientRefFor(accountID int64) string {
	const nonceChars = "abcdefghijklmnopqrstuvwxyz0123456789"
	nonce := make([]byte, 6)
	for i := range nonce {
		nonce[i] = nonceChars[rand.IntN(len(nonceChars))]
	}
	ref := fmt.Sprintf("pan-%d-%d-%s", accountID, time.Now().UTC().UnixMilli(), nonce)
	if len(ref) > 45 {
		ref = ref[:45]
	}
	return ref
}

// panMatches compares the submitted PAN with the provider's.
//
// Exact match is the normal case. The masking branch exists because the spec's
// own sample response returns "CXXPD1234H" for the top-level pan and an
// obviously masked "XXXXXX8398" for the Voter ID beside it — so a provider that
// masks interior characters is a documented possibility, and comparing that
// literally would fail every genuine PAN. When the provider's value contains X
// placeholders, the visible characters must all agree and at least six of the
// ten must be visible; below that the value carries too little information to
// call it a match rather than a coincidence.
//
// X is a legal character in a real PAN, so this only relaxes the comparison
// when the submitted PAN disagrees in exactly the masked positions.
func panMatches(submitted, provider string) bool {
	a := strings.ToUpper(strings.TrimSpace(submitted))
	b := strings.ToUpper(strings.TrimSpace(provider))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	visible := 0
	for i := range a {
		if b[i] == 'X' {
			continue
		}
		if a[i] != b[i] {
			return false
		}
		visible++
	}
	return visible >= 6
}

// nameMatches compares the submitted full name with the provider's, tolerating
// the ordinary noise between how a person types their name and how a bureau
// records it: case, punctuation, extra spaces, and up to maxDistance edits.
//
// It also accepts a reordering (GOPAL RAMESH KUMAR vs KUMAR GOPAL RAMESH) and a
// subset (a missing middle name), both of which are common enough in Indian
// records that rejecting them would fail real users at signup. normalizeName
// and levenshtein are shared with the OCR validator in pan_validator.go.
func nameMatches(submitted, provider string, maxDistance int) bool {
	a := normalizeName(submitted)
	b := normalizeName(provider)
	if a == "" || b == "" {
		return false
	}
	if a == b || levenshtein(a, b) <= maxDistance {
		return true
	}

	af := strings.Fields(a)
	bf := strings.Fields(b)
	if len(af) == 0 || len(bf) == 0 {
		return false
	}

	// Same words in a different order.
	if len(af) == len(bf) && sortedJoin(af) == sortedJoin(bf) {
		return true
	}

	// One is a subset of the other (a dropped middle name). Require every word
	// of the shorter to appear in the longer, and at least two words to match,
	// so a shared first name alone is not enough.
	shorter, longer := af, bf
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if len(shorter) < 2 {
		return false
	}
	for _, w := range shorter {
		found := false
		for _, l := range longer {
			if w == l || levenshtein(w, l) <= 1 {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sortedJoin(words []string) string {
	c := append([]string(nil), words...)
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j] < c[j-1]; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
	return strings.Join(c, " ")
}
