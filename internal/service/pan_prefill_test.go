package service

import "testing"

// TestPanMatches covers the comparison the whole signup gate rests on: too
// strict and legitimate users are locked out, too loose and it stops meaning
// anything.
func TestPanMatches(t *testing.T) {
	cases := []struct {
		name      string
		submitted string
		provider  string
		want      bool
	}{
		{"exact", "ABCDE1234F", "ABCDE1234F", true},
		{"case and space insensitive", " abcde1234f ", "ABCDE1234F", true},
		{"different pan", "ABCDE1234F", "ZZZZZ9999Z", false},
		{"one character off", "ABCDE1234F", "ABCDE1234G", false},
		{"empty provider", "ABCDE1234F", "", false},
		{"empty submitted", "", "ABCDE1234F", false},
		{"length mismatch", "ABCDE1234F", "ABCDE1234", false},

		// Masked provider values: the spec's own sample returns "CXXPD1234H"
		// alongside an obviously masked Voter ID, so this branch is documented
		// behaviour rather than defensive guesswork.
		{"masked interior, visible agree", "CDEPD1234H", "CXXPD1234H", true},
		{"masked interior, visible disagree", "CDEPD1234H", "CXXPD9999H", false},
		{"masked beyond the visibility floor", "ABCDE1234F", "AXXXX1XXXX", false},
		{"masked exactly at the floor", "ABCDE1234F", "AXCDX1234F", true},

		// X is legal in a real PAN, so an all-visible comparison still applies.
		{"literal X in both", "AXCDE1234F", "AXCDE1234F", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := panMatches(c.submitted, c.provider); got != c.want {
				t.Errorf("panMatches(%q, %q) = %v, want %v", c.submitted, c.provider, got, c.want)
			}
		})
	}
}

// TestNameMatches: bureau records and what a person types differ in ordinary,
// predictable ways. Each accepted case here is a real pattern in Indian name
// records; each rejected one is a different person.
func TestNameMatches(t *testing.T) {
	const distance = 2
	cases := []struct {
		name      string
		submitted string
		provider  string
		want      bool
	}{
		{"identical", "JOHN DOE", "JOHN DOE", true},
		{"case and spacing", "  john   doe ", "JOHN DOE", true},
		{"punctuation", "R. K. SHARMA", "R K SHARMA", true},
		{"typo inside the distance", "RAHUL SHARMA", "RAHUL SHARMAA", true},
		{"reordered words", "GOPAL RAMESH KUMAR", "KUMAR GOPAL RAMESH", true},
		{"dropped middle name", "RAHUL KUMAR SHARMA", "RAHUL SHARMA", true},
		{"extra middle name", "RAHUL SHARMA", "RAHUL KUMAR SHARMA", true},

		{"different person", "RAHUL SHARMA", "PRIYA MEHTA", false},
		{"shared first name only", "RAHUL SHARMA", "RAHUL MEHTA", false},
		{"shared surname only", "RAHUL SHARMA", "PRIYA SHARMA", false},
		{"empty submitted", "", "RAHUL SHARMA", false},
		{"empty provider", "RAHUL SHARMA", "", false},
		{"digits only normalizes away to empty", "12345", "RAHUL SHARMA", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nameMatches(c.submitted, c.provider, distance); got != c.want {
				t.Errorf("nameMatches(%q, %q) = %v, want %v", c.submitted, c.provider, got, c.want)
			}
		})
	}
}

// TestClientRefFor: the reference is echoed into a third party's logs, so it
// must carry no personal data and must satisfy their format rule.
func TestClientRefFor(t *testing.T) {
	ref := clientRefFor(42)
	// Also check the widest account id that can exist, since truncating to fit
	// the provider's limit must not be able to cut the nonce off entirely.
	for _, id := range []int64{1, 42, 9223372036854775807} {
		if got := clientRefFor(id); len(got) > 45 {
			t.Errorf("client ref %q for account %d is %d chars, provider allows 45", got, id, len(got))
		}
	}
	for _, r := range ref {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == ' '
		if !ok {
			t.Errorf("client ref %q contains %q, outside the provider's ^[a-zA-Z0-9 _-]*$", ref, r)
		}
	}
	if clientRefFor(42) == ref {
		t.Error("client ref should differ between calls; the provider requires it to be unique per request")
	}
}
