package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func testKeyRingCSV() string {
	key1 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	key2 := base64.StdEncoding.EncodeToString(bytes32(0x07))
	return "1:" + key1 + ",2:" + key2
}

func bytes32(fill byte) []byte {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill
	}
	return raw
}

func validProductionConfig() Config {
	return Config{
		Environment:          "production",
		MemberAllowedOrigins: []string{"https://app.example.com"},
		PublicAppBaseURL:     "https://app.example.com",
		RateLimitHMACKey:     strings.Repeat("k", 32),
		EmailDeliveryKeys:    testKeyRingCSV(),
		MailerProvider:       MailerProviderSMTP,
		MailFrom:             "no-reply@example.com",
		SMTPHost:             "smtp.example.com",
	}
}

func TestValidateProduction(t *testing.T) {
	cfg := validProductionConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
}

func TestValidateProductionFailures(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"missing origins", func(c *Config) { c.MemberAllowedOrigins = nil }},
		{"wildcard origin", func(c *Config) { c.MemberAllowedOrigins = []string{"*"} }},
		{"http base url", func(c *Config) { c.PublicAppBaseURL = "http://app.example.com" }},
		{"missing base url", func(c *Config) { c.PublicAppBaseURL = "" }},
		{"short hmac key", func(c *Config) { c.RateLimitHMACKey = "short" }},
		{"missing hmac key", func(c *Config) { c.RateLimitHMACKey = "" }},
		{"capture mailer", func(c *Config) { c.MailerProvider = MailerProviderCapture }},
		{"missing smtp host", func(c *Config) { c.SMTPHost = "" }},
		{"missing mail from", func(c *Config) { c.MailFrom = "" }},
		{"broken delivery keys", func(c *Config) { c.EmailDeliveryKeys = "1:not-base64" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validProductionConfig()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestValidateDevelopmentDefaults(t *testing.T) {
	cfg := Config{
		Environment:       "development",
		EmailDeliveryKeys: testKeyRingCSV(),
		MailerProvider:    MailerProviderCapture,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("minimal development config rejected: %v", err)
	}
}

func TestEmailKeyRing(t *testing.T) {
	cfg := Config{EmailDeliveryKeys: testKeyRingCSV()}
	keys, current, err := cfg.EmailKeyRing()
	if err != nil {
		t.Fatalf("EmailKeyRing: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %d, want 2", len(keys))
	}
	if current != "1" {
		t.Fatalf("current = %q, want first version 1", current)
	}

	cfg.EmailDeliveryCurrentKeyVersion = "2"
	_, current, err = cfg.EmailKeyRing()
	if err != nil || current != "2" {
		t.Fatalf("explicit current = %q err = %v", current, err)
	}

	cfg.EmailDeliveryCurrentKeyVersion = "9"
	if _, _, err := cfg.EmailKeyRing(); err == nil {
		t.Fatal("unknown current version must be rejected")
	}

	cfg.EmailDeliveryKeys = "1:" + base64.StdEncoding.EncodeToString([]byte("short"))
	if _, _, err := cfg.EmailKeyRing(); err == nil {
		t.Fatal("short key material must be rejected")
	}

	cfg.EmailDeliveryKeys = ""
	if _, _, err := cfg.EmailKeyRing(); err == nil {
		t.Fatal("empty key ring must be rejected")
	}
}
