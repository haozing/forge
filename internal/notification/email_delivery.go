// Package notification owns the reliable, encrypted email delivery pipeline.
// Domain services persist an email_deliveries row inside the business
// transaction; the worker claims rows with SKIP LOCKED leases, calls the
// Mailer outside any database transaction and never logs sensitive payloads.
package notification

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// Templates supported in phase 1.
const (
	TemplateOrganizationInvitation = "organization_invitation"
	TemplatePasswordReset          = "password_reset"
)

// Delivery statuses.
const (
	StatusPending   = "pending"
	StatusSending   = "sending"
	StatusSent      = "sent"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

const (
	MaxAttempts = 8
	LeasePeriod = 2 * time.Minute
)

// ErrNoPendingDelivery marks an empty claim so the worker loop can idle.
var ErrNoPendingDelivery = errors.New("no pending email delivery")

// KeyRing holds the versioned AES-256 delivery keys from the secret manager.
type KeyRing struct {
	// Keys maps key version (decimal string) to 32-byte key material.
	Keys map[string][]byte
	// Current is the version new deliveries use.
	Current string
}

// Cipher encrypts and decrypts delivery payloads with AES-256-GCM. The
// delivery id and template are bound as associated data so a ciphertext cannot
// be replayed against another delivery.
type Cipher struct {
	ring KeyRing
}

func NewCipher(ring KeyRing) (*Cipher, error) {
	if len(ring.Keys) == 0 || ring.Current == "" {
		return nil, errors.New("email delivery key ring is empty")
	}
	if _, ok := ring.Keys[ring.Current]; !ok {
		return nil, fmt.Errorf("current key version %q is not in the ring", ring.Current)
	}
	for version, key := range ring.Keys {
		if len(key) != 32 {
			return nil, fmt.Errorf("key version %q must be 32 bytes", version)
		}
	}
	return &Cipher{ring: ring}, nil
}

// Encrypt seals payload for storage in encrypted_payload.
func (c *Cipher) Encrypt(deliveryID, template string, plaintext []byte) (keyVersion string, ciphertext []byte, err error) {
	key := c.ring.Keys[c.ring.Current]
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", nil, err
	}
	aad := associatedData(deliveryID, template)
	sealed := gcm.Seal(nil, nonce, plaintext, aad)
	return c.ring.Current, append(nonce, sealed...), nil
}

// Decrypt opens a stored ciphertext with the key version recorded on the row.
func (c *Cipher) Decrypt(keyVersion, deliveryID, template string, stored []byte) ([]byte, error) {
	key, ok := c.ring.Keys[keyVersion]
	if !ok {
		return nil, fmt.Errorf("email delivery key version %q is unavailable", keyVersion)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(stored) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, sealed := stored[:gcm.NonceSize()], stored[gcm.NonceSize():]
	return gcm.Open(nil, nonce, sealed, associatedData(deliveryID, template))
}

func associatedData(deliveryID, template string) []byte {
	sum := sha256.Sum256([]byte(deliveryID + "\x00" + template))
	return sum[:]
}

// KeyVersionNumber converts the configured version string for storage.
func KeyVersionNumber(version string) (int32, error) {
	if version == "" {
		return 0, errors.New("empty key version")
	}
	var value int64
	for _, ch := range version {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("key version %q must be numeric", version)
		}
		value = value*10 + int64(ch-'0')
		if value > 1<<31-1 {
			return 0, fmt.Errorf("key version %q overflows", version)
		}
	}
	return int32(value), nil
}

// Message is what a Mailer sends. It carries no database identity beyond the
// provider idempotency key.
type Message struct {
	DeliveryID string
	Template   string
	To         string
	Subject    string
	Body       string
}

// Mailer adapts the outbound provider. Implementations must be safe for
// sequential use and must never be called while holding a database
// transaction.
type Mailer interface {
	Send(ctx context.Context, message Message) (providerMessageID string, err error)
}

// Classifier buckets provider failures into stable error codes for
// last_error_code; unexpected errors fall back to provider_error.
func ClassifyProviderError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrProviderTimeout):
		return "provider_timeout"
	case errors.Is(err, ErrProviderRateLimited):
		return "provider_rate_limited"
	case errors.Is(err, ErrProviderUnavailable):
		return "provider_unavailable"
	default:
		return "provider_error"
	}
}

var (
	ErrProviderTimeout      = errors.New("email provider timeout")
	ErrProviderRateLimited  = errors.New("email provider rate limited")
	ErrProviderUnavailable  = errors.New("email provider unavailable")
)

// RetryBackoff returns the exponential backoff for attempt n (1-based).
func RetryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 30 * time.Second
	for i := 1; i < attempt && delay < 30*time.Minute; i++ {
		delay *= 2
	}
	if delay > 30*time.Minute {
		return 30 * time.Minute
	}
	return delay
}

// LeaseToken generates the worker lease marker.
func LeaseToken(workerID string) string {
	raw := make([]byte, 8)
	binary.LittleEndian.PutUint64(raw, uint64(time.Now().UnixNano()))
	return workerID + "-" + base64.RawURLEncoding.EncodeToString(raw)
}
