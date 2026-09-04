package objectstore

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

type OSSConfig struct {
	Region   string
	Bucket   string
	Endpoint string
	Prefix   string
}

type OSS struct {
	client *oss.Client
	bucket string
	prefix string
}

func NewOSS(cfg OSSConfig) (*OSS, error) {
	if strings.TrimSpace(cfg.Region) == "" {
		return nil, errors.New("OSS_REGION is required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("OSS_BUCKET is required")
	}
	prefix, err := normalizePrefix(cfg.Prefix)
	if err != nil {
		return nil, fmt.Errorf("invalid OSS_PREFIX: %w", err)
	}
	provider := credentials.NewEnvironmentVariableCredentialsProvider()
	sdkConfig := oss.LoadDefaultConfig().
		WithCredentialsProvider(provider).
		WithRegion(strings.TrimSpace(cfg.Region))
	if strings.TrimSpace(cfg.Endpoint) != "" {
		sdkConfig = sdkConfig.WithEndpoint(strings.TrimSpace(cfg.Endpoint))
	}
	return &OSS{
		client: oss.NewClient(sdkConfig),
		bucket: strings.TrimSpace(cfg.Bucket),
		prefix: prefix,
	}, nil
}

func (s *OSS) Put(ctx context.Context, object Object) (ObjectRef, error) {
	if s == nil || s.client == nil {
		return ObjectRef{}, errors.New("OSS object store is not initialized")
	}
	key, err := s.validateKey(object.Key)
	if err != nil {
		return ObjectRef{}, err
	}
	if object.Body == nil {
		return ObjectRef{}, errors.New("object body is required")
	}
	if object.ContentLength < 0 {
		return ObjectRef{}, errors.New("object content length cannot be negative")
	}
	contentType := object.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	request := &oss.PutObjectRequest{
		Bucket:          oss.Ptr(s.bucket),
		Key:             oss.Ptr(key),
		Body:            object.Body,
		ContentType:     oss.Ptr(contentType),
		ContentLength:   oss.Ptr(object.ContentLength),
		ForbidOverwrite: oss.Ptr("true"),
	}
	_, err = s.client.PutObject(ctx, request)
	if err != nil {
		return ObjectRef{}, fmt.Errorf("put OSS object: %w", err)
	}
	return ObjectRef{Key: key}, nil
}

func (s *OSS) Get(ctx context.Context, ref ObjectRef) (ObjectReader, error) {
	if s == nil || s.client == nil {
		return ObjectReader{}, errors.New("OSS object store is not initialized")
	}
	key, err := s.validateKey(ref.Key)
	if err != nil {
		return ObjectReader{}, err
	}
	request := &oss.GetObjectRequest{
		Bucket: oss.Ptr(s.bucket),
		Key:    oss.Ptr(key),
	}
	// G5 responsive variants: the closed server-side enum maps to OSS image
	// processing (resize / webp). Empty Process returns the original bytes.
	if ref.Process != "" {
		request.Process = oss.Ptr(ref.Process)
	}
	result, err := s.client.GetObject(ctx, request)
	if err != nil {
		return ObjectReader{}, fmt.Errorf("get OSS object: %w", err)
	}
	if result == nil || result.Body == nil {
		return ObjectReader{}, errors.New("OSS returned an empty object body")
	}
	return ObjectReader{
		Body:          result.Body,
		ContentType:   stringValue(result.ContentType),
		ContentLength: result.ContentLength,
		ETag:          stringValue(result.ETag),
	}, nil
}

func (s *OSS) Delete(ctx context.Context, ref ObjectRef) error {
	if s == nil || s.client == nil {
		return errors.New("OSS object store is not initialized")
	}
	key, err := s.validateKey(ref.Key)
	if err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &oss.DeleteObjectRequest{
		Bucket: oss.Ptr(s.bucket),
		Key:    oss.Ptr(key),
	}); err != nil {
		return fmt.Errorf("delete OSS object: %w", err)
	}
	return nil
}

func (s *OSS) validateKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") {
		return "", errors.New("object key must be a relative path")
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == ".." || segment == "." || segment == "" {
			return "", errors.New("object key contains an invalid path segment")
		}
	}
	cleaned := path.Clean(key)
	if cleaned != key || (s.prefix != "" && !strings.HasPrefix(key, s.prefix)) {
		return "", errors.New("object key is outside the configured prefix")
	}
	return key, nil
}

func normalizePrefix(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", errors.New("prefix must be a relative path")
	}
	value = strings.TrimSuffix(value, "/")
	if value == "" {
		return "", errors.New("prefix must contain a path segment")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." || segment == "." || segment == "" {
			return "", errors.New("prefix contains an invalid path segment")
		}
	}
	return value + "/", nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
