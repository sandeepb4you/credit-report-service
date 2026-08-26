package config

import "testing"

// The three-way precedence here is easy to get subtly wrong, and getting it
// wrong is quiet: borrowing the Credit Analytics credentials when the stub was
// wanted sends a real, billable lookup against a real person's mobile number,
// and forcing the stub when real credentials were wanted marks accounts VERIFIED
// on the strength of nothing.

func TestResolvePrefillCredentials(t *testing.T) {
	cases := []struct {
		name       string
		cfg        DigitapConfig
		wantID     string
		wantSecret string
		wantStub   bool
	}{
		{
			name: "empty prefill borrows the credit-analytics pair",
			cfg: DigitapConfig{
				ClientID: "ca-id", ClientSecret: "ca-secret",
				Prefill: PrefillConfig{ClientID: "", ClientSecret: ""},
			},
			wantID: "ca-id", wantSecret: "ca-secret", wantStub: false,
		},
		{
			name: "an explicit prefill pair wins over the credit-analytics pair",
			cfg: DigitapConfig{
				ClientID: "ca-id", ClientSecret: "ca-secret",
				Prefill: PrefillConfig{ClientID: "pf-id", ClientSecret: "pf-secret"},
			},
			wantID: "pf-id", wantSecret: "pf-secret", wantStub: false,
		},
		{
			// The UAT case: credit-analytics talks to a real upstream while PAN
			// verification runs offline, because the UAT client id has no
			// name-lookup service.
			name: "the sentinel forces the stub even with working credentials",
			cfg: DigitapConfig{
				ClientID: "ca-id", ClientSecret: "ca-secret",
				Prefill: PrefillConfig{ClientID: "stub"},
			},
			wantID: "", wantSecret: "", wantStub: true,
		},
		{
			name: "the sentinel is case- and space-insensitive",
			cfg: DigitapConfig{
				ClientID: "ca-id", ClientSecret: "ca-secret",
				Prefill: PrefillConfig{ClientID: "  STUB "},
			},
			wantID: "", wantSecret: "", wantStub: true,
		},
		{
			// Nothing configured anywhere: still the stub, but by emptiness
			// rather than by request, so forcedStub stays false — the boot log
			// distinguishes "you asked for this" from "you forgot to set it".
			name:   "no credentials at all is not a forced stub",
			cfg:    DigitapConfig{},
			wantID: "", wantSecret: "", wantStub: false,
		},
		{
			// Whitespace is not a credential; it must not beat the fallback and
			// then be sent as a client id.
			name: "whitespace-only prefill id falls back",
			cfg: DigitapConfig{
				ClientID: "ca-id", ClientSecret: "ca-secret",
				Prefill: PrefillConfig{ClientID: "   "},
			},
			wantID: "ca-id", wantSecret: "ca-secret", wantStub: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, secret, stub := tc.cfg.ResolvePrefillCredentials()
			if id != tc.wantID {
				t.Errorf("clientID = %q, want %q", id, tc.wantID)
			}
			if secret != tc.wantSecret {
				t.Errorf("clientSecret = %q, want %q", secret, tc.wantSecret)
			}
			if stub != tc.wantStub {
				t.Errorf("forcedStub = %v, want %v", stub, tc.wantStub)
			}
		})
	}
}

// TestResolvePrefillCredentials_StubYieldsEmptyID pins the contract between this
// resolver and digitap.NewPrefill, which selects the stub on an empty ClientID.
// If the sentinel ever returned "stub" verbatim instead of "", it would be sent
// upstream as a client id and the stub would never engage.
func TestResolvePrefillCredentials_StubYieldsEmptyID(t *testing.T) {
	cfg := DigitapConfig{
		ClientID: "ca-id", ClientSecret: "ca-secret",
		Prefill: PrefillConfig{ClientID: PrefillStubSentinel},
	}
	id, _, stub := cfg.ResolvePrefillCredentials()
	if !stub {
		t.Fatal("sentinel did not force the stub")
	}
	if id != "" {
		t.Errorf("clientID = %q, want empty so digitap.NewPrefill selects the stub", id)
	}
}
