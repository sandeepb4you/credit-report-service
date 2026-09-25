package config

import "testing"

// Production mode with no credentials would run on the stub gateway, which
// accepts ANY webhook signature — anyone could POST "payment succeeded" and have
// an order fulfilled. The service must refuse to start instead.
func TestCashfreeValidateRefusesLiveModeWithoutKeys(t *testing.T) {
	cases := []struct {
		name    string
		cfg     CashfreeConfig
		wantErr bool
	}{
		{"sandbox without keys is the dev stub", CashfreeConfig{Mode: "sandbox"}, false},
		{"production without keys", CashfreeConfig{Mode: "production"}, true},
		{"production with only an id", CashfreeConfig{Mode: "production", ClientID: "id"}, true},
		{"production with keys", CashfreeConfig{Mode: "production", ClientID: "id", ClientSecret: "s"}, false},
		{"an unknown mode", CashfreeConfig{Mode: "live"}, true},
		{"half a sandbox pair", CashfreeConfig{
			Mode: "production", ClientID: "id", ClientSecret: "s",
			Sandbox: CashfreeCredentials{ClientID: "TEST1"},
		}, true},
		{"live keys plus sandbox keys", CashfreeConfig{
			Mode: "production", ClientID: "id", ClientSecret: "s",
			Sandbox: CashfreeCredentials{ClientID: "TEST1", ClientSecret: "t"},
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
