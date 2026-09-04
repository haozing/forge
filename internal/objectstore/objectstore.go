package objectstore

import (
	"context"
	"io"
)

// ObjectStore is intentionally narrower than the OSS client. Domain services
// use object keys and streams; they never need provider URLs or credentials.
type ObjectStore interface {
	Put(context.Context, Object) (ObjectRef, error)
	Get(context.Context, ObjectRef) (ObjectReader, error)
	Delete(context.Context, ObjectRef) error
}

type Object struct {
	Key           string
	Body          io.Reader
	ContentType   string
	ContentLength int64
}

type ObjectRef struct {
	Key string
	// Process carries an optional provider-side image processing directive
	// (G5 responsive variants). It is always a server-mapped closed enum —
	// never raw user input — and implementations without processing support
	// ignore it and return the original object.
	Process string
}

type ObjectReader struct {
	Body          io.ReadCloser
	ContentType   string
	ContentLength int64
	ETag          string
}
