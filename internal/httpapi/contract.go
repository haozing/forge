package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// writeETag applies the representation version used by member-side resources.
// Keep this separate from JSON encoding so stream and download handlers can use
// the same conditional-write semantics.
func writeETag(w http.ResponseWriter, etag string) {
	etag = strings.TrimSpace(etag)
	if etag == "" {
		return
	}
	if !strings.HasPrefix(etag, "\"") {
		etag = "\"" + etag + "\""
	}
	w.Header().Set("ETag", etag)
}

func ifMatchMatches(r *http.Request, current string) bool {
	if r == nil {
		return false
	}
	wanted := strings.TrimSpace(r.Header.Get("If-Match"))
	if wanted == "" || wanted == "*" {
		return wanted == "*"
	}
	return strings.Trim(wanted, "\"") == strings.Trim(current, "\"")
}

func representationETag(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func requestID() string {
	return time.Now().UTC().Format("20060102T150405.000000000Z07:00")
}
