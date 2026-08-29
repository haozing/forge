package notification

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
	"time"
)

// SMTPMailer sends through a standard SMTP provider using the delivery id as
// the provider idempotency reference.
type SMTPMailer struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
	Timeout  time.Duration
}

func (m SMTPMailer) Send(ctx context.Context, message Message) (string, error) {
	if m.Host == "" || m.From == "" {
		return "", ErrProviderUnavailable
	}
	addr := m.Host + ":" + m.Port
	headers := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <%s@agentchunzhi>\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n",
		m.From, message.To, mimeEncodeSubject(message.Subject),
		message.DeliveryID, time.Now().UTC().Format(time.RFC1123Z))
	body := headers + message.Body + "\r\n"
	auth := smtp.PlainAuth("", m.Username, m.Password, m.Host)
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- smtp.SendMail(addr, auth, m.From, []string{message.To}, []byte(body))
	}()
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	select {
	case err := <-sendErr:
		if err != nil {
			text := err.Error()
			switch {
			case strings.Contains(text, "421"), strings.Contains(text, "450"), strings.Contains(text, "451"):
				return "", ErrProviderUnavailable
			case strings.Contains(text, "rate limit"), strings.Contains(text, "too many"):
				return "", ErrProviderRateLimited
			}
			return "", err
		}
		return message.DeliveryID, nil
	case <-ctx.Done():
		return "", ErrProviderTimeout
	}
}
