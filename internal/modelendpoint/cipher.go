package modelendpoint

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const credentialCipherVersion byte = 1

type CredentialCipher struct {
	aead  cipher.AEAD
	keyID string
}

func NewCredentialCipher(encodedKey string) (*CredentialCipher, error) {
	encodedKey = strings.TrimSpace(encodedKey)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("AGENT_MODEL_SECRET_ENCRYPTION_KEY must be a base64-encoded 32-byte key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create credential AEAD: %w", err)
	}
	digest := sha256.Sum256(key)
	return &CredentialCipher{aead: aead, keyID: hex.EncodeToString(digest[:8])}, nil
}

func (c *CredentialCipher) KeyID() string {
	if c == nil {
		return ""
	}
	return c.keyID
}

func (c *CredentialCipher) Encrypt(plaintext string, additionalData []byte) ([]byte, error) {
	if c == nil || c.aead == nil || strings.TrimSpace(plaintext) == "" {
		return nil, errors.New("credential cipher or plaintext is empty")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate credential nonce: %w", err)
	}
	result := make([]byte, 1, 1+len(nonce)+len(plaintext)+c.aead.Overhead())
	result[0] = credentialCipherVersion
	result = append(result, nonce...)
	result = c.aead.Seal(result, nonce, []byte(plaintext), additionalData)
	return result, nil
}

func (c *CredentialCipher) Decrypt(payload, additionalData []byte) (string, error) {
	if c == nil || c.aead == nil || len(payload) < 1+c.aead.NonceSize()+c.aead.Overhead() {
		return "", errors.New("invalid encrypted credential")
	}
	if payload[0] != credentialCipherVersion {
		return "", errors.New("unsupported encrypted credential version")
	}
	nonceEnd := 1 + c.aead.NonceSize()
	plaintext, err := c.aead.Open(nil, payload[1:nonceEnd], payload[nonceEnd:], additionalData)
	if err != nil {
		return "", errors.New("decrypt model credential")
	}
	return string(plaintext), nil
}

func CredentialAdditionalData(organizationID, endpointID string) []byte {
	return []byte("agentchunzhi:model-endpoint:" + organizationID + ":" + endpointID)
}
