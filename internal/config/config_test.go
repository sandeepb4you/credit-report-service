package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	cfgContent := `server:
  port: 9090
auth:
  jwt-secret: file-secret
  otp:
    length: 8
log:
  level: debug
  format: json
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgContent), 0644); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want 9090", cfg.Server.Port)
	}
	if cfg.Auth.JWTSecret != "file-secret" {
		t.Errorf("Auth.JWTSecret = %q, want file-secret", cfg.Auth.JWTSecret)
	}
	if cfg.Auth.OTP.Length != 8 {
		t.Errorf("Auth.OTP.Length = %d, want 8", cfg.Auth.OTP.Length)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" {
		t.Errorf("Log.Format = %q, want json", cfg.Log.Format)
	}
}

func TestLoad_WithProfile(t *testing.T) {
	dir := t.TempDir()
	base := `server:
  port: 8080
auth:
  jwt-secret: base-secret
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(base), 0644)

	profile := `server:
  port: 3000
auth:
  jwt-secret: dev-secret
`
	os.WriteFile(filepath.Join(dir, "config.dev.yaml"), []byte(profile), 0644)

	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	cfg, err := Load("dev")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 3000 {
		t.Errorf("Server.Port = %d, want 3000 (profile overlay)", cfg.Server.Port)
	}
	if cfg.Auth.JWTSecret != "dev-secret" {
		t.Errorf("Auth.JWTSecret = %q, want dev-secret (profile overlay)", cfg.Auth.JWTSecret)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("SERVER_PORT", "4000")
	t.Setenv("LOG_LEVEL", "warn")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 4000 {
		t.Errorf("Server.Port = %d, want 4000 (from env)", cfg.Server.Port)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("Log.Level = %q, want warn (from env)", cfg.Log.Level)
	}
}

func TestLoad_AdminEmailsFromEnv(t *testing.T) {
	t.Setenv("AUTH_ADMIN_EMAILS", "admin@example.com,  super@example.com  ")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Auth.AdminEmails) != 2 {
		t.Fatalf("len(AdminEmails) = %d, want 2", len(cfg.Auth.AdminEmails))
	}
	if cfg.Auth.AdminEmails[0] != "admin@example.com" {
		t.Errorf("AdminEmails[0] = %q", cfg.Auth.AdminEmails[0])
	}
	if cfg.Auth.AdminEmails[1] != "super@example.com" {
		t.Errorf("AdminEmails[1] = %q", cfg.Auth.AdminEmails[1])
	}
}

func TestLoad_AdminEmailsEmpty(t *testing.T) {
	t.Setenv("AUTH_ADMIN_EMAILS", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Auth.AdminEmails) != 0 {
		t.Errorf("AdminEmails = %v, want empty", cfg.Auth.AdminEmails)
	}
}

func TestLoad_NoConfigFile(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load without config file should succeed: %v", err)
	}
	// Should have defaults applied.
	if cfg.Server.Port == 0 {
		t.Error("Server.Port should have a default")
	}
	if cfg.Auth.JWTSecret == "" {
		t.Error("Auth.JWTSecret should have a default")
	}
}

func TestLoad_BadProfileFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("server:\n  port: 8080\n"), 0644)
	os.WriteFile(filepath.Join(dir, "config.bad.yaml"), []byte("server: [invalid\n"), 0644)

	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	_, err := Load("bad")
	if err == nil {
		t.Fatal("expected error for invalid profile YAML")
	}
}

// ---- master-code profile gate ----
//
// The master code is an authentication bypass for every OTP flow, so the thing
// worth testing is not that it loads but that a non-local profile refuses to
// start at all.

func writeMasterCodeConfig(t *testing.T, code string) {
	t.Helper()
	dir := t.TempDir()
	content := `auth:
  jwt-secret: test-secret
  otp:
    length: 4
    master-code: "` + code + `"
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(origDir) })
}

func TestLoad_MasterCode_RejectedOutsideLocalProfile(t *testing.T) {
	// The empty profile is the important case: a deployment runs config.yaml with
	// no overlay, so "" must not count as local.
	for _, profile := range []string{"", "prod", "staging"} {
		writeMasterCodeConfig(t, "1234")
		if _, err := Load(profile); err == nil {
			t.Errorf("Load(%q) accepted a master code outside a local profile", profile)
		}
	}
}

func TestLoad_MasterCode_AllowedOnLocalProfiles(t *testing.T) {
	for _, profile := range []string{"dev", "local"} {
		writeMasterCodeConfig(t, "1234")
		cfg, err := Load(profile)
		if err != nil {
			t.Fatalf("Load(%q) = %v, want nil", profile, err)
		}
		if cfg.Auth.OTP.MasterCode != "1234" {
			t.Errorf("Load(%q) master code = %q, want 1234", profile, cfg.Auth.OTP.MasterCode)
		}
	}
}

func TestLoad_MasterCode_MustMatchOTPLength(t *testing.T) {
	// The app's code field stops accepting input at auth.otp.length, so a longer
	// master code is one nobody can type.
	writeMasterCodeConfig(t, "123456")
	if _, err := Load("dev"); err == nil {
		t.Error("Load accepted a master code longer than auth.otp.length")
	}
}

func TestLoad_NoMasterCode_LoadsAnywhere(t *testing.T) {
	writeMasterCodeConfig(t, "")
	if _, err := Load("prod"); err != nil {
		t.Errorf("Load(prod) with no master code = %v, want nil", err)
	}
}

// ---- digitap.log-request-curl profile gate ----
//
// Same shape as the master-code gate above, and for the same reason: the flag
// writes the account's PAN, full name and mobile number plus our Digitap client
// secret into the application log, so what matters is that a deployment refuses
// to boot with it rather than that it loads.

func writeCurlLogConfig(t *testing.T, enabled bool) {
	t.Helper()
	dir := t.TempDir()
	value := "false"
	if enabled {
		value = "true"
	}
	content := `auth:
  jwt-secret: test-secret
digitap:
  log-request-curl: ` + value + `
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(origDir) })
}

func TestLoad_CurlLogging_RejectedOutsideLocalProfile(t *testing.T) {
	// "" is the case that matters most: a deployment runs config.yaml with no
	// overlay, so an empty profile must not count as local.
	for _, profile := range []string{"", "prod", "staging"} {
		writeCurlLogConfig(t, true)
		if _, err := Load(profile); err == nil {
			t.Errorf("Load(%q) accepted digitap.log-request-curl outside a local profile", profile)
		}
	}
}

func TestLoad_CurlLogging_AllowedOnLocalProfiles(t *testing.T) {
	for _, profile := range []string{"dev", "local"} {
		writeCurlLogConfig(t, true)
		cfg, err := Load(profile)
		if err != nil {
			t.Fatalf("Load(%q) = %v, want nil", profile, err)
		}
		if !cfg.Digitap.LogRequestCurl {
			t.Errorf("Load(%q) LogRequestCurl = false, want true", profile)
		}
	}
}

func TestLoad_CurlLogging_OffLoadsAnywhere(t *testing.T) {
	writeCurlLogConfig(t, false)
	cfg, err := Load("prod")
	if err != nil {
		t.Errorf("Load(prod) with the flag off = %v, want nil", err)
	}
	if cfg != nil && cfg.Digitap.LogRequestCurl {
		t.Error("LogRequestCurl defaulted to true; it must be opt-in")
	}
}

// ---- demo.enabled profile gate ----
//
// The most consequential of the three local-only flags: demo mode auto-verifies
// any PAN submitted to POST /api/kyc/pan with provider='demo' and no provider
// call, so nothing checks that the PAN exists, belongs to the user, or matches
// their mobile — while credit analytics still gates on the VERIFIED status it
// hands out. A deployment must refuse to boot rather than run that quietly.

func writeDemoConfig(t *testing.T, enabled bool) {
	t.Helper()
	dir := t.TempDir()
	value := "false"
	if enabled {
		value = "true"
	}
	content := `auth:
  jwt-secret: test-secret
demo:
  enabled: ` + value + `
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(origDir) })
}

func TestLoad_DemoMode_RejectedOutsideLocalProfile(t *testing.T) {
	// "" is the case that matters: the deployed stack sets no APP_PROFILE, so
	// this is the exact configuration a real deploy would run.
	for _, profile := range []string{"", "prod", "staging", "uat"} {
		writeDemoConfig(t, true)
		if _, err := Load(profile); err == nil {
			t.Errorf("Load(%q) accepted demo.enabled outside a local profile", profile)
		}
	}
}

func TestLoad_DemoMode_AllowedOnLocalProfiles(t *testing.T) {
	for _, profile := range []string{"dev", "local"} {
		writeDemoConfig(t, true)
		cfg, err := Load(profile)
		if err != nil {
			t.Fatalf("Load(%q) = %v, want nil", profile, err)
		}
		if !cfg.Demo.Enabled {
			t.Errorf("Load(%q) Demo.Enabled = false, want true", profile)
		}
	}
}

func TestLoad_DemoMode_OffLoadsAnywhere(t *testing.T) {
	writeDemoConfig(t, false)
	if _, err := Load("prod"); err != nil {
		t.Errorf("Load(prod) with demo mode off = %v, want nil", err)
	}
}

func TestLoad_DemoMode_DefaultsOff(t *testing.T) {
	// Nothing in the file mentions demo at all. The compiled default must be
	// the safe one, so an environment that forgets to set it is not exposed.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("auth:\n  jwt-secret: test-secret\n"), 0644); err != nil {
		t.Fatal(err)
	}
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(origDir) })

	cfg, err := Load("prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Demo.Enabled {
		t.Error("demo.enabled defaulted to true; auto-verifying every PAN must never be the default")
	}
}

// TestLoad_DemoMode_EnvCannotSmuggleItIn covers the route that actually caused
// this: the flag was true in deploy/.env.staging as DEMO_ENABLED, not in any
// yaml. An env var must hit the same wall.
func TestLoad_DemoMode_EnvCannotSmuggleItIn(t *testing.T) {
	writeDemoConfig(t, false)
	t.Setenv("DEMO_ENABLED", "true")
	if _, err := Load(""); err == nil {
		t.Error("DEMO_ENABLED=true was accepted under a non-local profile")
	}
}

func TestLoad_CurlLogging_DefaultsOff(t *testing.T) {
	// Nothing in the file mentions the key at all.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("auth:\n  jwt-secret: test-secret\n"), 0644); err != nil {
		t.Fatal(err)
	}
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(origDir) })

	cfg, err := Load("prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Digitap.LogRequestCurl {
		t.Error("digitap.log-request-curl defaulted to true; PII logging must never be the default")
	}
}
