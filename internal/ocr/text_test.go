package ocr

import "testing"

func TestExtractPAN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"no pan", "Hello world 12345", ""},
		{"basic", "INCOME TAX\nABCDE1234F", "ABCDE1234F"},
		{"embedded", "pan: abcde1234f done", "ABCDE1234F"},
		{"multi", "AAAAA1111A and BBBBB2222B", "AAAAA1111A"}, // first wins
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractPAN(tc.in)
			if got != tc.want {
				t.Fatalf("ExtractPAN(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractName_NameMarker(t *testing.T) {
	text := "INCOME TAX DEPARTMENT\nNAME: RAHUL SHARMA\nABCDE1234F"
	got := ExtractName(text)
	want := "RAHUL SHARMA"
	if got != want {
		t.Fatalf("ExtractName = %q, want %q", got, want)
	}
}

func TestExtractName_FallbackLongest(t *testing.T) {
	// No NAME marker; should pick the longest line that looks like a name.
	text := "INCOME TAX DEPARTMENT\nFOO BAR BAZ\nABCDE1234F"
	got := ExtractName(text)
	want := "FOO BAR BAZ"
	if got != want {
		t.Fatalf("ExtractName = %q, want %q", got, want)
	}
}

func TestExtractName_Empty(t *testing.T) {
	if got := ExtractName("   \n   "); got != "" {
		t.Fatalf("ExtractName = %q, want empty", got)
	}
}
