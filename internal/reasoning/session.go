package reasoning

import (
	"encoding/json"
	"net/http"
	"strings"
)

// SessionID extracts an explicit stable session id for the cache scope. Order of preference:
//  1. Common conversation/thread/session-id headers from the inbound request.
//  2. Explicit payload conversation/thread/session identifiers.
//
// User-only metadata and prompt hashes are intentionally not used. They are weak
// identifiers that can collide across unrelated clients or conversations. Returns
// empty string if no explicit conversation/session signal is available; callers
// should treat empty as "no session, do not cache" rather than collapse all
// traffic into one scope.
func SessionID(headers http.Header, payload []byte) string {
	for _, h := range []string{"X-Session-Id", "X-Conversation-Id", "X-Thread-Id", "X-Amp-Thread-Id"} {
		if v := strings.TrimSpace(headers.Get(h)); v != "" {
			return v
		}
	}
	if len(payload) > 0 {
		var root map[string]any
		if err := json.Unmarshal(payload, &root); err == nil {
			for _, key := range []string{"conversation_id", "session_id", "thread_id"} {
				if v, ok := root[key].(string); ok && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
			if md, ok := root["metadata"].(map[string]any); ok {
				for _, key := range []string{"conversation_id", "session_id", "thread_id"} {
					if v, ok := md[key].(string); ok && strings.TrimSpace(v) != "" {
						return strings.TrimSpace(v)
					}
				}
			}
		}
	}
	return ""
}
