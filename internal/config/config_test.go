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
