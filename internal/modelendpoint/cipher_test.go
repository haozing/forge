package modelendpoint

import (
	"encoding/base64"
	"testing"
)

func TestCredentialCipherRoundTripAndAAD(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher, err := NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aad := CredentialAdditionalData("org-1", "endpoint-1")
	payload, err := cipher.Encrypt("secret-value", aad)
	if err != nil {
		t.Fatal(err)
	}
	got, err := cipher.Decrypt(payload, aad)
	if err != nil {
		t.Fatal(err)
	}
	if got != "secret-value" {
		t.Fatalf("got %q", got)
	}
	if _, err := cipher.Decrypt(payload, CredentialAdditionalData("org-2", "endpoint-1")); err == nil {
		t.Fatal("expected AAD mismatch to fail")
	}
}

func TestCredentialCipherRejectsInvalidKey(t *testing.T) {
	if _, err := NewCredentialCipher(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("expected invalid key error")
	}
}

func TestValidateBaseURL(t *testing.T) {
	allowed := []string{"api.openai.com", "*.models.example.com"}
	for _, value := range []string{"https://api.openai.com/v1", "https://cn.models.example.com/v1"} {
		if err := ValidateBaseURL(value, allowed); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"http://api.openai.com/v1", "https://127.0.0.1/v1", "https://other.example.com/v1", "https://user:pass@api.openai.com/v1"} {
		if err := ValidateBaseURL(value, allowed); err == nil {
			t.Fatalf("expected %s to fail", value)
		}
	}
}
