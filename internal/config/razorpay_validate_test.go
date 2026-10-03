package config

import "testing"

// Production mode with no credentials would run on the stub gateway, which
// accepts ANY webhook signature — anyone could POST "payment captured" and have
// an order fulfilled. The service must refuse to start instead. A key for the
// wrong environment is refused too: Razorpay picks the environment from the
// key, so a live key in a sandbox deployment takes real money for "tests".
func TestRazorpayValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     RazorpayConfig
		wantErr bool
	}{
		{"sandbox without keys is the dev stub", RazorpayConfig{Mode: "sandbox"}, false},
		{"production without keys", RazorpayConfig{Mode: "production"}, true},
		{"production with only an id", RazorpayConfig{Mode: "production", KeyID: "rzp_live_x"}, true},
		{"production with live keys", RazorpayConfig{Mode: "production", KeyID: "rzp_live_x", KeySecret: "s"}, false},
		{"production with a test key", RazorpayConfig{Mode: "production", KeyID: "rzp_test_x", KeySecret: "s"}, true},
		{"sandbox with a live key", RazorpayConfig{Mode: "sandbox", KeyID: "rzp_live_x", KeySecret: "s"}, true},
		{"sandbox with test keys", RazorpayConfig{Mode: "sandbox", KeyID: "rzp_test_x", KeySecret: "s"}, false},
		{"an unknown mode", RazorpayConfig{Mode: "live"}, true},
		{"half a sandbox pair", RazorpayConfig{
			Mode: "production", KeyID: "rzp_live_x", KeySecret: "s",
			Sandbox: RazorpayCredentials{KeyID: "rzp_test_y"},
		}, true},
		{"a live key in the sandbox block", RazorpayConfig{
			Mode: "production", KeyID: "rzp_live_x", KeySecret: "s",
			Sandbox: RazorpayCredentials{KeyID: "rzp_live_y", KeySecret: "t"},
		}, true},
		{"live keys plus sandbox keys", RazorpayConfig{
			Mode: "production", KeyID: "rzp_live_x", KeySecret: "s",
			Sandbox: RazorpayCredentials{KeyID: "rzp_test_y", KeySecret: "t"},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.validate(); (err != nil) != tc.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
