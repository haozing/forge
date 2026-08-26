package transcription

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// TencentProvider calls the Tencent Cloud ASR 3.0 SentenceRecognition API.
// It signs each request server-side with TC3-HMAC-SHA256; no credential is
// exposed to callers or persisted with the transcription job.
type TencentProvider struct {
	SecretID  string
	SecretKey string
	Region    string
	Engine    string
	Client    *http.Client
}

func (p TencentProvider) Transcribe(ctx context.Context, body io.Reader, filename, mediaType, language string) (Result, error) {
	if strings.TrimSpace(p.SecretID) == "" || strings.TrimSpace(p.SecretKey) == "" {
		return Result{}, errors.New("Tencent ASR credentials are not configured")
	}
	if body == nil {
		return Result{}, errors.New("Tencent ASR media body is required")
	}
	data, err := io.ReadAll(io.LimitReader(body, 3<<20+1))
	if err != nil {
		return Result{}, fmt.Errorf("read Tencent ASR media: %w", err)
	}
	if len(data) == 0 || len(data) > 3<<20 {
		return Result{}, errors.New("Tencent ASR media must be between 1 byte and 3 MiB")
	}
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), ".")
	if format == "" {
		format = formatFromMediaType(mediaType)
	}
	if format == "" {
		format = "wav"
	}
	engine := strings.TrimSpace(p.Engine)
	if engine == "" {
		engine = "16k_zh"
	}
	payload := map[string]any{
		"ProjectId":      0,
		"EngSerViceType": engine,
		"SourceType":     1,
		"VoiceFormat":    format,
		"Data":           base64.StdEncoding.EncodeToString(data),
		"DataLen":        len(data),
		"ConvertNumMode": 1,
		"FilterDirty":    0,
		"FilterModal":    0,
		"FilterPunc":     0,
		"WordInfo":       0,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("encode Tencent ASR request: %w", err)
	}
	host := "asr.tencentcloudapi.com"
	service := "asr"
	action := "SentenceRecognition"
	version := "2019-06-14"
	region := strings.TrimSpace(p.Region)
	if region == "" {
		region = "ap-beijing"
	}
	timestamp := time.Now().UTC().Unix()
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	hashedPayload := sha256Hex(bodyBytes)
	canonicalHeaders := "content-type:application/json; charset=utf-8\nhost:" + host + "\n"
	signedHeaders := "content-type;host"
	canonicalRequest := "POST\n/\n\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + hashedPayload
	scope := date + "/" + service + "/tc3_request"
	stringToSign := "TC3-HMAC-SHA256\n" + fmt.Sprint(timestamp) + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))
	secretDate := hmacSHA256([]byte("TC3"+p.SecretKey), date)
	secretService := hmacSHA256(secretDate, service)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))
	authorization := "TC3-HMAC-SHA256 Credential=" + p.SecretID + "/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/", bytes.NewReader(bodyBytes))
	if err != nil {
		return Result{}, fmt.Errorf("create Tencent ASR request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Host", host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Region", region)
	req.Header.Set("X-TC-Timestamp", fmt.Sprint(timestamp))
	req.Header.Set("Authorization", authorization)
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("call Tencent ASR: %w", err)
	}
	defer resp.Body.Close()
	var output struct {
		Response struct {
			Result string `json:"Result"`
			Error  *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&output); err != nil {
		return Result{}, fmt.Errorf("decode Tencent ASR response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("Tencent ASR returned %s", resp.Status)
	}
	if output.Response.Error != nil {
		return Result{}, fmt.Errorf("Tencent ASR %s: %s", output.Response.Error.Code, output.Response.Error.Message)
	}
	text := strings.TrimSpace(output.Response.Result)
	if text == "" {
		return Result{}, errors.New("Tencent ASR returned empty text")
	}
	return Result{Text: text, Language: language}, nil
}

func hmacSHA256(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func formatFromMediaType(mediaType string) string {
	if strings.HasPrefix(mediaType, "audio/") {
		return strings.TrimPrefix(mediaType, "audio/")
	}
	return "wav"
}
