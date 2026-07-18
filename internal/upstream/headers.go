package upstream

import (
	"net/http"
	"strings"
)

// gatewayHeaderPrefixes mirrors well-known AI-gateway prefixes that we strip from
// upstream responses so downstream clients can't identify the proxy as a gateway.
var gatewayHeaderPrefixes = []string{
	"x-litellm-",
	"helicone-",
	"x-portkey-",
	"cf-aig-",
	"x-kong-",
	"x-bt-",
}

// hopByHopHeaders are RFC 7230 §6.1 hop-by-hop names plus a few that must not
// leak from upstream to the caller (Set-Cookie) or that we re-derive (Content-*).
var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	"Set-Cookie":          {},
	"Content-Length":      {},
	"Content-Encoding":    {},
}

// privacySensitiveIntermediaryHeaders are removed from relayed upstream responses
// because their values can reveal internal topology, intermediary hostnames,
// software names, versions, or comments. These are NOT hop-by-hop under RFC 7230
// §6.1 semantics; they are filtered as a distinct privacy-sensitive
// intermediary-metadata category to keep the proxy's boundary opaque to
// downstream clients and captured logs.
var privacySensitiveIntermediaryHeaders = map[string]struct{}{
	"Via": {},
}

// reservedOutboundHeaders are never allowed to be supplied by user-controlled
// config/request headers. Provider auth and protocol-required headers are set by
// the proxy itself, before/after filtering as appropriate.
var reservedOutboundHeaders = map[string]struct{}{
	"Authorization":       {},
	"Accept-Encoding":     {},
	"X-Api-Key":           {},
	"Host":                {},
	"Content-Length":      {},
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Proxy-Connection":    {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	"Cookie":              {},
	"Set-Cookie":          {},
	"Forwarded":           {},
	"X-Real-Ip":           {},
}

// IsReservedOutboundHeader reports whether a configured/request-time header is
// security-sensitive, hop-by-hop, or proxy-derived and must not be forwarded to
// upstream from user-controlled sources.
func IsReservedOutboundHeader(name string) bool {
	canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
	if canonical == "" {
		return true
	}
	if _, blocked := reservedOutboundHeaders[canonical]; blocked {
		return true
	}
	lower := strings.ToLower(canonical)
	return strings.HasPrefix(lower, "x-forwarded-")
}

// FilterHeaders returns a copy of src minus hop-by-hop, security-sensitive,
// privacy-sensitive intermediary metadata, and known gateway-detector headers.
// Returns nil if nothing remains.
func FilterHeaders(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	connectionScoped := connectionScopedHeaders(src)
	dst := make(http.Header)
	for key, values := range src {
		canonical := http.CanonicalHeaderKey(key)
		if _, blocked := hopByHopHeaders[canonical]; blocked {
			continue
		}
		if _, scoped := connectionScoped[canonical]; scoped {
			continue
		}
		if _, intermediary := privacySensitiveIntermediaryHeaders[canonical]; intermediary {
			continue
		}
		lower := strings.ToLower(key)
		match := false
		for _, prefix := range gatewayHeaderPrefixes {
			if strings.HasPrefix(lower, prefix) {
				match = true
				break
			}
		}
		if match {
			continue
		}
		dst[key] = values
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

func connectionScopedHeaders(src http.Header) map[string]struct{} {
	scoped := make(map[string]struct{})
	for _, raw := range src.Values("Connection") {
		for _, token := range strings.Split(raw, ",") {
			name := strings.TrimSpace(token)
			if name == "" {
				continue
			}
			scoped[http.CanonicalHeaderKey(name)] = struct{}{}
		}
	}
	return scoped
}

// CopyHeaders writes filtered headers from src into dst without overwriting
// values already set on dst (so handlers can lock in Content-Type before relaying).
func CopyHeaders(dst http.Header, src http.Header) {
	if src == nil {
		return
	}
	filtered := FilterHeaders(src)
	for key, values := range filtered {
		if dst.Get(key) != "" {
			continue
		}
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}
