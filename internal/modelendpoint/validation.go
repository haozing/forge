package modelendpoint

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

var (
	ErrInvalidInput = errors.New("invalid model endpoint input")
	ErrNotFound     = errors.New("model endpoint not found")
	ErrConflict     = errors.New("model endpoint conflict")
	ErrUnavailable  = errors.New("model endpoint unavailable")
)

func ValidateBaseURL(raw string, allowedHosts []string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ErrInvalidInput
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if ip := net.ParseIP(host); ip != nil && isPrivateIP(ip) {
		return ErrInvalidInput
	}
	if !hostAllowed(host, allowedHosts) {
		return ErrInvalidInput
	}
	return nil
}

func hostAllowed(host string, allowedHosts []string) bool {
	for _, allowed := range allowedHosts {
		allowed = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(allowed, ".")))
		if allowed == host {
			return true
		}
		if strings.HasPrefix(allowed, "*.") {
			suffix := strings.TrimPrefix(allowed, "*")
			if strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".") {
				return true
			}
		}
	}
	return false
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func validateOptions(options Options) bool {
	if options.TimeoutSeconds < 1 || options.TimeoutSeconds > 600 || options.MaxInputTokens < 1 || options.MaxInputTokens > 2_000_000 || options.MaxOutputTokens < 1 || options.MaxOutputTokens > 200_000 {
		return false
	}
	if options.Temperature != nil && (*options.Temperature < 0 || *options.Temperature > 2) {
		return false
	}
	switch options.ThinkingMode {
	case "", "enabled", "disabled":
	default:
		return false
	}
	switch options.StructuredOutputMode {
	case "disabled", "json_object", "json_schema":
		return true
	default:
		return false
	}
}
