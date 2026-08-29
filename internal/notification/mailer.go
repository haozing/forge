package notification

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// CaptureMailer is the development/test mailer: it records messages in memory
// instead of contacting a provider. Delivery tokens stay in the encrypted
// payload until the delivery completes; they are never logged.
type CaptureMailer struct {
	mu       sync.Mutex
	Messages []Message
}

// Send appends the message and echoes the delivery id as the provider id.
func (m *CaptureMailer) Send(_ context.Context, message Message) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Messages = append(m.Messages, message)
	return "capture-" + message.DeliveryID, nil
}

func base64StdEncode(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

func mimeEncodeSubject(subject string) string {
	if isASCII(subject) {
		return strings.ReplaceAll(subject, "\r\n", " ")
	}
	return "=?UTF-8?B?" + base64StdEncode(subject) + "?="
}

func isASCII(value string) bool {
	for _, ch := range value {
		if ch > 127 {
			return false
		}
	}
	return true
}

// JoinBaseURL builds invitation and password reset links exclusively from the
// configured base URL; request hosts are never trusted.
func JoinBaseURL(base, path string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("PUBLIC_APP_BASE_URL is invalid")
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/"), nil
}
