package logging

import (
	"regexp"
	"strings"
)

// Patterns for common secret shapes. Replacement preserves the non-secret
// delimiters/key names and replaces only the literal secret with a placeholder.
var redactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)([A-Za-z0-9\-\._=+/]+)`),
	regexp.MustCompile(`(?i)(authorization:\s*)([A-Za-z0-9\-\._=+/]{16,})`),
	regexp.MustCompile(`(?i)(x-api-key:\s*)([A-Za-z0-9\-\._=+/]+)`),
	regexp.MustCompile(`(?i)(anthropic-api-key:\s*)([A-Za-z0-9\-\._=+/]+)`),
	regexp.MustCompile(`(?i)("api_key"\s*:\s*")([^"]+)(")`),
	regexp.MustCompile(`(?i)("apiKey"\s*:\s*")([^"]+)(")`),
	regexp.MustCompile(`(?i)("(?:access_token|refresh_token|id_token|authorization|credential|password|api_key|apikey|token|secret|auth|key)"\s*:\s*")([^"]+)(")`),
	regexp.MustCompile(`(?i)(^|[?&#;\s])((?:access_token|refresh_token|id_token|authorization|credential|password|api_key|apikey|token|secret|auth|key)=)([^&#\s"'` + "`" + `<>]*)`),
	regexp.MustCompile(`(sk-[A-Za-z0-9_\-]{16,})`),
}

// Redact masks secrets in s, replacing matched values with ***.
// If a pattern has three groups, the third (closing quote/delimiter) is preserved verbatim.
func Redact(s string) string {
	out := s
	for _, re := range redactPatterns {
		out = re.ReplaceAllStringFunc(out, func(match string) string {
			groups := re.FindStringSubmatch(match)
			switch len(groups) {
			case 2:
				// whole-token match (e.g. sk-...)
				return "***"
			case 3:
				return groups[1] + "***"
			case 4:
				if strings.Contains(groups[2], "=") && !strings.Contains(groups[1], `"`) {
					return groups[1] + groups[2] + "***"
				}
				return groups[1] + "***" + groups[3]
			case 5:
				return groups[1] + groups[2] + "***"
			default:
				return "***"
			}
		})
	}
	return out
}

// RedactBytes is a convenience wrapper around Redact for byte slices.
func RedactBytes(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	return []byte(Redact(string(b)))
}
