// Command prefillcheck calls Digitap's Mobile to Prefill API for one mobile
// number and prints what comes back.
//
// It exists to separate two questions that the app conflates: "is our
// integration right" and "does the provider have this person". Running it needs
// no database, no emulator and no account — just credentials.
//
//	go run ./cmd/prefillcheck -mobile 9876543210
//	go run ./cmd/prefillcheck -mobile 9876543210 -uat
//	go run ./cmd/prefillcheck -mobile 9876543210 -raw
//
// Credentials come from DIGITAP_PREFILL_CLIENT_ID / _SECRET, falling back to
// DIGITAP_CLIENT_ID / _SECRET, exactly as the server resolves them.
//
// The response is real personal data about whoever owns the number. Use your
// own, and note that a live lookup is billed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"credit-report-service/internal/digitap"
)

func main() {
	mobile := flag.String("mobile", "", "Indian mobile number to look up (10 digits, or +91…)")
	uat := flag.Bool("uat", false, "use the UAT host instead of production")
	raw := flag.Bool("raw", false, "print the decoded result as JSON")
	flag.Parse()

	if strings.TrimSpace(*mobile) == "" {
		fmt.Fprintln(os.Stderr, "usage: prefillcheck -mobile <10-digit number> [-uat] [-raw]")
		os.Exit(2)
	}

	id := firstNonEmpty(os.Getenv("DIGITAP_PREFILL_CLIENT_ID"), os.Getenv("DIGITAP_CLIENT_ID"))
	secret := firstNonEmpty(os.Getenv("DIGITAP_PREFILL_CLIENT_SECRET"), os.Getenv("DIGITAP_CLIENT_SECRET"))
	if id == "" {
		fmt.Fprintln(os.Stderr, "no credentials: set DIGITAP_PREFILL_CLIENT_ID/_SECRET (or DIGITAP_CLIENT_ID/_SECRET).")
		fmt.Fprintln(os.Stderr, "Without them this would run the offline stub, which answers for every number and proves nothing.")
		os.Exit(2)
	}

	base := "https://api.digitap.ai/"
	if *uat {
		base = "https://svcdemo.digitap.work/"
	}

	client := digitap.NewPrefill(digitap.PrefillConfig{
		BaseURL:      base,
		ClientID:     id,
		ClientSecret: secret,
		Timeout:      30 * time.Second,
	})

	fmt.Printf("host   : %s\n", base)
	fmt.Printf("mobile : %s\n", maskTail(*mobile))

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	out, err := client.Lookup(ctx, "prefillcheck-cli", *mobile)
	if err != nil {
		fmt.Printf("\nFAILED: %v\n", err)
		switch {
		case strings.Contains(err.Error(), "not enabled"):
			fmt.Println("\nMobile to Prefill (or its name-lookup service) is not provisioned on this")
			fmt.Println("client id. Digitap enables products per client id — ask your RM to add it.")
		case strings.Contains(err.Error(), "authentication"):
			fmt.Println("\nThe credentials were rejected. Note the UAT and production environments")
			fmt.Println("use different client ids; try the other one with -uat.")
		}
		os.Exit(1)
	}

	fmt.Printf("\nresult_code : %d\n", out.ResultCode)
	fmt.Printf("request_id  : %s\n", out.RequestID)
	fmt.Printf("message     : %s\n", out.Message)

	switch out.ResultCode {
	case digitap.PrefillNoRecord:
		fmt.Println("\nNo record against this number. In the app this is a provider gap: the PAN")
		fmt.Println("is stored PENDING and the user is let through rather than blocked.")
		return
	case digitap.PrefillNameMissing:
		fmt.Println("\nA record exists but carries no name, so there is nothing to compare against.")
		return
	}

	if out.Result == nil {
		fmt.Println("\nNo result body.")
		return
	}

	fmt.Println("\n-- what the app compares against --")
	fmt.Printf("name : %s\n", out.Result.Name)
	fmt.Printf("pan  : %s\n", out.Result.BestPAN())
	if strings.ContainsRune(out.Result.BestPAN(), 'X') {
		fmt.Println("       ^ contains X: the provider is masking the PAN. panMatches() handles")
		fmt.Println("         this by treating X as a wildcard — worth confirming that is intended.")
	}

	if *raw {
		b, _ := json.MarshalIndent(out.Result, "", "  ")
		fmt.Printf("\n-- decoded result --\n%s\n", b)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// maskTail keeps the output pasteable into a ticket without reproducing a full
// mobile number in it.
func maskTail(s string) string {
	if len(s) <= 4 {
		return s
	}
	return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
}
