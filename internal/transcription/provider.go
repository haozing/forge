package transcription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// Provider is deliberately server-side: callers pass an already authorized
// object stream, never a browser URL or provider credential.
type Provider interface {
	Transcribe(context.Context, io.Reader, string, string, string) (Result, error)
}

type Result struct {
	Text     string
	Language string
}

type HTTPProvider struct {
	Endpoint string
	Token    string
	Model    string
	Client   *http.Client
}

func (p HTTPProvider) Transcribe(ctx context.Context, body io.Reader, filename, mediaType, language string) (Result, error) {
	if strings.TrimSpace(p.Endpoint) == "" || strings.TrimSpace(p.Token) == "" {
		return Result{}, errors.New("ASR provider is not configured")
	}
	if body == nil {
		return Result{}, errors.New("ASR media body is required")
	}
	endpoint, err := url.Parse(p.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return Result{}, errors.New("ASR endpoint is invalid")
	}
	var payload bytes.Buffer
	form := multipart.NewWriter(&payload)
	part, err := form.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return Result{}, fmt.Errorf("create ASR file part: %w", err)
	}
	if _, err := io.Copy(part, body); err != nil {
		return Result{}, fmt.Errorf("copy ASR media: %w", err)
	}
	model := p.Model
	if model == "" {
		model = "whisper-1"
	}
	_ = form.WriteField("model", model)
	if language != "" {
		_ = form.WriteField("language", language)
	}
	if err := form.Close(); err != nil {
		return Result{}, fmt.Errorf("close ASR form: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), &payload)
	if err != nil {
		return Result{}, fmt.Errorf("create ASR request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Content-Type", form.FormDataContentType())
	if mediaType != "" {
		req.Header.Set("X-Source-Media-Type", mediaType)
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("call ASR provider: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Result{}, fmt.Errorf("ASR provider returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var result struct {
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Result{}, fmt.Errorf("decode ASR response: %w", err)
	}
	result.Text = strings.TrimSpace(result.Text)
	if result.Text == "" {
		return Result{}, errors.New("ASR provider returned empty text")
	}
	return Result{Text: result.Text, Language: strings.TrimSpace(result.Language)}, nil
}
