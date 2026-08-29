package notification

import (
	"bytes"
	"testing"
	"time"
)

func TestRetryBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 30 * time.Second}, // clamped to first attempt
		{1, 30 * time.Second}, // first retry after 30s
		{2, time.Minute},      // exponential
		{3, 2 * time.Minute},
		{4, 4 * time.Minute},
		{6, 16 * time.Minute},
		{7, 30 * time.Minute},  // cap
		{20, 30 * time.Minute}, // cap holds
	}
	for _, tc := range cases {
		if got := RetryBackoff(tc.attempt); got != tc.want {
			t.Errorf("RetryBackoff(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	key := bytes.Repeat([]byte{0x42}, 32)
	oldKey := bytes.Repeat([]byte{0x07}, 32)
	cipher, err := NewCipher(KeyRing{Keys: map[string][]byte{"2": key, "1": oldKey}, Current: "2"})
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return cipher
}

func TestCipherRoundTrip(t *testing.T) {
	cipher := testCipher(t)
	plaintext := []byte(`{"token":"raw-token","link":"https://app.example.com/invite/accept?token=raw-token"}`)

	version, ciphertext, err := cipher.Encrypt("delivery-1", TemplateOrganizationInvitation, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if version != "2" {
		t.Fatalf("Encrypt version = %q, want current 2", version)
	}
	if bytes.Contains(ciphertext, []byte("raw-token")) {
		t.Fatal("ciphertext must not contain the plaintext")
	}
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext must differ from the plaintext")
	}
	decrypted, err := cipher.Decrypt(version, "delivery-1", TemplateOrganizationInvitation, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("round trip mismatch: %q", decrypted)
	}

	// The same ciphertext cannot be replayed against another delivery or
	// template: the delivery id and template are bound as associated data.
	if _, err := cipher.Decrypt(version, "delivery-2", TemplateOrganizationInvitation, ciphertext); err == nil {
		t.Fatal("opening under a different delivery id must fail")
	}
	if _, err := cipher.Decrypt(version, "delivery-1", TemplatePasswordReset, ciphertext); err == nil {
		t.Fatal("opening under a different template must fail")
	}
	if _, err := cipher.Decrypt("1", "delivery-1", TemplateOrganizationInvitation, ciphertext); err == nil {
		t.Fatal("opening under the wrong key version must fail")
	}
}

func TestNewCipherRejectsBrokenRings(t *testing.T) {
	if _, err := NewCipher(KeyRing{}); err == nil {
		t.Fatal("empty ring must be rejected")
	}
	key := bytes.Repeat([]byte{1}, 31)
	if _, err := NewCipher(KeyRing{Keys: map[string][]byte{"1": key}, Current: "1"}); err == nil {
		t.Fatal("short key must be rejected")
	}
	goodKey := bytes.Repeat([]byte{1}, 32)
	if _, err := NewCipher(KeyRing{Keys: map[string][]byte{"1": goodKey}, Current: "2"}); err == nil {
		t.Fatal("current version outside the ring must be rejected")
	}
}

func TestJoinBaseURL(t *testing.T) {
	link, err := JoinBaseURL("https://app.example.com", "/invite/accept?token=abc")
	if err != nil {
		t.Fatalf("JoinBaseURL: %v", err)
	}
	if link != "https://app.example.com/invite/accept?token=abc" {
		t.Fatalf("link = %q", link)
	}
	if _, err := JoinBaseURL("not-a-url", "/x"); err == nil {
		t.Fatal("invalid base URL must be rejected")
	}
}

func TestKeyVersionNumber(t *testing.T) {
	if _, err := KeyVersionNumber(""); err == nil {
		t.Fatal("empty version must be rejected")
	}
	if _, err := KeyVersionNumber("v2"); err == nil {
		t.Fatal("non-numeric version must be rejected")
	}
	version, err := KeyVersionNumber("007")
	if err != nil {
		t.Fatalf("KeyVersionNumber: %v", err)
	}
	if version != 7 {
		t.Fatalf("version = %d, want 7", version)
	}
}

func TestCaptureMailerAndTemplates(t *testing.T) {
	mailer := &CaptureMailer{}
	message := Message{DeliveryID: "d-1", Template: TemplatePasswordReset, To: "a@b.co", Subject: "s", Body: "b"}
	providerID, err := mailer.Send(nil, message)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if providerID != "capture-d-1" {
		t.Fatalf("provider id = %q", providerID)
	}
	if len(mailer.Messages) != 1 {
		t.Fatalf("captured %d messages", len(mailer.Messages))
	}

	renderer := NewTextTemplateRenderer()
	rendered, err := renderer.Render(TemplateOrganizationInvitation, "Acme", map[string]any{
		"link": "https://app.example.com/invite/accept?token=xyz", "workspace_name": "Docs",
	})
	if err != nil {
		t.Fatalf("Render invitation: %v", err)
	}
	if !bytes.Contains([]byte(rendered.Body), []byte("https://app.example.com/invite/accept?token=xyz")) {
		t.Fatalf("body missing link: %q", rendered.Body)
	}
	if !bytes.Contains([]byte(rendered.Body), []byte("workspace \"Docs\"")) {
		t.Fatalf("body missing workspace: %q", rendered.Body)
	}
	if _, err := renderer.Render("unknown-template", "Acme", map[string]any{"link": "https://x"}); err == nil {
		t.Fatal("unknown template must be rejected")
	}
	if _, err := renderer.Render(TemplatePasswordReset, "Acme", map[string]any{}); err == nil {
		t.Fatal("payload without link must be rejected")
	}
}
