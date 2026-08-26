package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentchunzhi/internal/config"
	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/transcription"
)

type result struct {
	OSS *checkResult `json:"oss,omitempty"`
	ASR *checkResult `json:"asr,omitempty"`
}

type checkResult struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "oss" && os.Args[1] != "asr") {
		fmt.Fprintln(os.Stderr, "usage: provider-acceptance oss|asr")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cfg := config.Load()
	output := result{}
	var err error
	switch os.Args[1] {
	case "oss":
		output.OSS, err = checkOSS(ctx, cfg)
	case "asr":
		output.ASR, err = checkASR(ctx, cfg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s acceptance failed: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(output)
}

func checkOSS(ctx context.Context, cfg config.Config) (*checkResult, error) {
	if strings.TrimSpace(cfg.OSSRegion) == "" || strings.TrimSpace(cfg.OSSBucket) == "" {
		return nil, errors.New("OSS_REGION and OSS_BUCKET are required")
	}
	objects, err := objectstore.NewOSS(objectstore.OSSConfig{
		Region: cfg.OSSRegion, Bucket: cfg.OSSBucket, Endpoint: cfg.OSSEndpoint, Prefix: cfg.OSSPrefix,
	})
	if err != nil {
		return nil, err
	}
	randomID := make([]byte, 12)
	if _, err := rand.Read(randomID); err != nil {
		return nil, fmt.Errorf("generate acceptance object id: %w", err)
	}
	name := "provider-acceptance-" + hex.EncodeToString(randomID) + ".txt"
	prefix := strings.TrimSuffix(strings.TrimSpace(cfg.OSSPrefix), "/")
	key := name
	if prefix != "" {
		key = prefix + "/" + name
	}
	payload := []byte("agentchunzhi external OSS acceptance " + name)
	if _, err := objects.Put(ctx, objectstore.Object{Key: key, Body: bytes.NewReader(payload), ContentType: "text/plain", ContentLength: int64(len(payload))}); err != nil {
		return nil, err
	}
	defer func() { _ = objects.Delete(context.Background(), objectstore.ObjectRef{Key: key}) }()
	reader, err := objects.Get(ctx, objectstore.ObjectRef{Key: key})
	if err != nil {
		return nil, err
	}
	readback, readErr := io.ReadAll(reader.Body)
	closeErr := reader.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read OSS object: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close OSS object: %w", closeErr)
	}
	if !bytes.Equal(payload, readback) {
		return nil, errors.New("OSS readback does not match uploaded bytes")
	}
	if err := objects.Delete(ctx, objectstore.ObjectRef{Key: key}); err != nil {
		return nil, err
	}
	return &checkResult{Status: "passed", Detail: "put/get/delete content round-trip verified"}, nil
}

func checkASR(ctx context.Context, cfg config.Config) (*checkResult, error) {
	samplePath := strings.TrimSpace(os.Getenv("ASR_SAMPLE_FILE"))
	if samplePath == "" {
		return nil, errors.New("ASR_SAMPLE_FILE is required")
	}
	tencent := strings.EqualFold(cfg.ASRProvider, "tencent") || (cfg.TencentSecretID != "" && cfg.TencentSecretKey != "")
	if tencent {
		if strings.TrimSpace(cfg.TencentSecretID) == "" || strings.TrimSpace(cfg.TencentSecretKey) == "" {
			return nil, errors.New("TENCENTCLOUD_SECRET_ID and TENCENTCLOUD_SECRET_KEY are required")
		}
	} else if strings.TrimSpace(cfg.ASREndpoint) == "" || strings.TrimSpace(cfg.ASRToken) == "" {
		return nil, errors.New("ASR_ENDPOINT and ASR_TOKEN are required")
	}
	file, err := os.Open(samplePath)
	if err != nil {
		return nil, fmt.Errorf("open ASR sample: %w", err)
	}
	defer file.Close()
	mediaType := mime.TypeByExtension(filepath.Ext(samplePath))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	var provider transcription.Provider
	if strings.EqualFold(cfg.ASRProvider, "tencent") || (cfg.TencentSecretID != "" && cfg.TencentSecretKey != "") {
		provider = transcription.TencentProvider{SecretID: cfg.TencentSecretID, SecretKey: cfg.TencentSecretKey, Region: cfg.ASRRegion, Engine: cfg.ASREngine}
	} else {
		provider = transcription.HTTPProvider{Endpoint: cfg.ASREndpoint, Token: cfg.ASRToken, Model: cfg.ASRModel}
	}
	transcript, err := provider.Transcribe(ctx, file, filepath.Base(samplePath), mediaType, "")
	if err != nil {
		return nil, err
	}
	return &checkResult{Status: "passed", Detail: fmt.Sprintf("non-empty transcript returned (%d chars)", len([]rune(transcript.Text)))}, nil
}
