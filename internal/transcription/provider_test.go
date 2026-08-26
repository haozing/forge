package transcription

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPProviderTranscribeMultipartContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("X-Source-Media-Type"); got != "audio/wav" {
			t.Errorf("source media type = %q", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		if got := r.FormValue("model"); got != "test-model" {
			t.Errorf("model = %q", got)
		}
		if got := r.FormValue("language"); got != "zh" {
			t.Errorf("language = %q", got)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("file part: %v", err)
		}
		defer file.Close()
		if header.Filename != "sample.wav" {
			t.Errorf("filename = %q", header.Filename)
		}
		body, _ := io.ReadAll(file)
		if string(body) != "audio-bytes" {
			t.Errorf("body = %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"会议纪要","language":"zh"}`))
	}))
	defer server.Close()

	result, err := (HTTPProvider{Endpoint: server.URL, Token: "test-token", Model: "test-model"}).Transcribe(
		context.Background(), strings.NewReader("audio-bytes"), "sample.wav", "audio/wav", "zh",
	)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if result.Text != "会议纪要" || result.Language != "zh" {
		t.Fatalf("result = %#v", result)
	}
}

func TestHTTPProviderRejectsEmptyTranscript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":""}`))
	}))
	defer server.Close()
	_, err := (HTTPProvider{Endpoint: server.URL, Token: "token"}).Transcribe(context.Background(), strings.NewReader("x"), "x.wav", "audio/wav", "")
	if err == nil || !strings.Contains(err.Error(), "empty text") {
		t.Fatalf("expected empty transcript error, got %v", err)
	}
}
