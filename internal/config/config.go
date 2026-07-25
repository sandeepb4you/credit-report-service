// Package config loads the service configuration using Viper.
//
// It mirrors the previous Spring application.yml: a base config.yaml plus an
// optional named profile (e.g. "dev") overlaid on top, with environment
// variables taking the highest precedence.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the top-level configuration tree.
type Config struct {
	Server       ServerConfig       `mapstructure:"server"`
	DB           DBConfig           `mapstructure:"db"`
	Mail         MailConfig         `mapstructure:"mail"`
	Auth         AuthConfig         `mapstructure:"auth"`
	Multipart    MultipartConfig    `mapstructure:"multipart"`
	Registration RegistrationConfig `mapstructure:"registration"`
	Digitap      DigitapConfig      `mapstructure:"digitap"`
}

// DigitapConfig holds credentials and endpoint settings for the Digitap Credit
// Analytics API (spec V2.7). When ClientID is empty, the service runs the
// client in an offline stub mode.
type DigitapConfig struct {
	BaseURL      string        `mapstructure:"base-url"`
	ClientID     string        `mapstructure:"client-id"`
	ClientSecret string        `mapstructure:"client-secret"`
	Timeout      time.Duration `mapstructure:"timeout"`
}

// AuthConfig holds JWT signing settings for the email/password auth flow.
type AuthConfig struct {
	JWTSecret   string        `mapstructure:"jwt-secret"`
	JWTTTL      time.Duration `mapstructure:"jwt-ttl"`
	OTP         OTPConfig     `mapstructure:"otp"`
	AdminEmails []string      `mapstructure:"admin-emails"`
}

type ServerConfig struct {
	Port           int    `mapstructure:"port"`
	MaxRequestBody string `mapstructure:"max-request-body"`
}

type DBConfig struct {
	URL         string `mapstructure:"url"`
	Username    string `mapstructure:"username"`
	Password    string `mapstructure:"password"`
	MaxPoolSize int    `mapstructure:"max-pool-size"`
	MinIdle     int    `mapstructure:"min-idle"`
	// When set, takes precedence over URL/Username/Password.
	DSN string `mapstructure:"dsn"`
}

type MailConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	From     string `mapstructure:"from"`
}

type MultipartConfig struct {
	MaxFileSize    string `mapstructure:"max-file-size"`
	MaxRequestSize string `mapstructure:"max-request-size"`
}

type RegistrationConfig struct {
	PanImageDir string    `mapstructure:"pan-image-dir"`
	OTP         OTPConfig `mapstructure:"otp"`
	PAN         PANConfig `mapstructure:"pan"`
	OCR         OCRConfig `mapstructure:"ocr"`
}

type OTPConfig struct {
	Length         int           `mapstructure:"length"`
	TTL            time.Duration `mapstructure:"ttl"`
	ResendCooldown time.Duration `mapstructure:"resend-cooldown"`
	MaxAttempts    int           `mapstructure:"max-attempts"`
	MaxSends       int           `mapstructure:"max-sends"`
}

type PANConfig struct {
	NameMatchDistance int `mapstructure:"name-match-distance"`
}

type OCRConfig struct {
	Provider      string  `mapstructure:"provider"`
	MinConfidence float64 `mapstructure:"min-confidence"`
}

// Load reads config.yaml (and config.<profile>.yaml if profile is non-empty),
// then overlays environment variables. Env keys are uppercased, dot-separated
// keys become underscore-separated (e.g. registration.otp.length ->
// REGISTRATION_OTP_LENGTH). Env values override file values.
func Load(profile string) (*Config, error) {
	v := viper.New()
	v.SetConfigName("config") // config.yaml / config.yml
	v.SetConfigType("yaml")
	v.AddConfigPath(".") // project root when run from repo
	v.AddConfigPath("./config")

	setDefaults(v)

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, fmt.Errorf("read config: %w", err)
		}
		// No base file — proceed; env vars + defaults may still cover it.
	}

	if profile != "" {
		v.SetConfigName(fmt.Sprintf("config.%s", profile))
		// Merge rather than replace so the dev file only overrides what it sets.
		if err := v.MergeInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) {
				return nil, fmt.Errorf("merge profile %q: %w", profile, err)
			}
		}
	}

	// Bind every key under the same name (env: REGISTRATION_OTP_LENGTH).
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
	// Make sure nested keys resolve from env without needing the full prefix.
	_ = bindEnvForKeys(v, allKeys())

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Viper won't split a comma-separated env value into a slice, so handle
	// AUTH_ADMIN_EMAILS explicitly. The file form is already a YAML list.
	if raw := os.Getenv("AUTH_ADMIN_EMAILS"); raw != "" {
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				out = append(out, t)
			}
		}
		cfg.Auth.AdminEmails = out
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.max-request-body", "10MB")

	v.SetDefault("db.max-pool-size", 10)
	v.SetDefault("db.min-idle", 2)

	v.SetDefault("mail.port", 587)
	v.SetDefault("mail.from", "Scorr.club <noreply@scorr.club>")

	v.SetDefault("auth.jwt-secret", "dev-insecure-change-me")
	v.SetDefault("auth.jwt-ttl", "720h") // 30 days
	v.SetDefault("auth.otp.length", 6)
	v.SetDefault("auth.otp.ttl", "10m")
	v.SetDefault("auth.otp.resend-cooldown", "60s")
	v.SetDefault("auth.otp.max-attempts", 5)
	v.SetDefault("auth.otp.max-sends", 5)
	// Admin allowlist: accounts whose email matches get role=admin at verify/login.
	// Defaults to empty (no admins). Set via AUTH_ADMIN_EMAILS=a@x.com,b@y.com.
	v.SetDefault("auth.admin-emails", []string{})

	v.SetDefault("multipart.max-file-size", "5MB")
	v.SetDefault("multipart.max-request-size", "10MB")

	v.SetDefault("registration.pan-image-dir", "./data/pan-images")
	v.SetDefault("registration.otp.length", 6)
	v.SetDefault("registration.otp.ttl", "5m")
	v.SetDefault("registration.otp.resend-cooldown", "60s")
	v.SetDefault("registration.otp.max-attempts", 5)
	v.SetDefault("registration.otp.max-sends", 5)
	v.SetDefault("registration.pan.name-match-distance", 2)
	v.SetDefault("registration.ocr.provider", "stub")
	v.SetDefault("registration.ocr.min-confidence", 0.8)

	// Digitap Credit Analytics API. Empty client-id -> offline stub client.
	v.SetDefault("digitap.base-url", "https://apidemo.digitap.work/")
	v.SetDefault("digitap.client-id", "")
	v.SetDefault("digitap.client-secret", "")
	v.SetDefault("digitap.timeout", "30s")
}

func allKeys() []string {
	return []string{
		"server.port", "server.max-request-body",
		"db.url", "db.username", "db.password", "db.dsn", "db.max-pool-size", "db.min-idle",
		"mail.host", "mail.port", "mail.username", "mail.password", "mail.from",
		"auth.jwt-secret", "auth.jwt-ttl",
		"auth.otp.length", "auth.otp.ttl", "auth.otp.resend-cooldown",
		"auth.otp.max-attempts", "auth.otp.max-sends",
		"auth.admin-emails",
		"multipart.max-file-size", "multipart.max-request-size",
		"registration.pan-image-dir",
		"registration.otp.length", "registration.otp.ttl",
		"registration.otp.resend-cooldown", "registration.otp.max-attempts",
		"registration.otp.max-sends",
		"registration.pan.name-match-distance",
		"registration.ocr.provider", "registration.ocr.min-confidence",
		"digitap.base-url", "digitap.client-id", "digitap.client-secret", "digitap.timeout",
	}
}

// bindEnvForKeys makes viper check the environment for each dot-key explicitly,
// because AutomaticEnv only resolves keys that the file already contains or
// that have a default. With this, e.g. setting REGISTRATION_OTP_LENGTH=8 works
// even without a file entry.
func bindEnvForKeys(v *viper.Viper, keys []string) error {
	for _, k := range keys {
		if err := v.BindEnv(k); err != nil {
			return err
		}
	}
	return nil
}
