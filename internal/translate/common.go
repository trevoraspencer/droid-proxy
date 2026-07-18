package translate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/sjson"
)

func copyIfPresent(dst, src map[string]any, keys ...string) {
	for _, k := range keys {
		if v, ok := src[k]; ok {
			dst[k] = v
		}
	}
}

// marshalWithExtraArgs serializes a translated payload and then applies
// model extra_args in sorted key order via sjson, matching the semantics of
// applyUpstreamPayloadOverrides on native routes: keys are sjson paths (a key
// "a.b" nests), and application order is deterministic so identical requests
// always produce byte-identical upstream bodies.
func marshalWithExtraArgs(out map[string]any, extraArgs map[string]any) ([]byte, error) {
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(extraArgs))
	for k := range extraArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if next, err := sjson.SetBytes(raw, k, extraArgs[k]); err == nil {
			raw = next
		}
	}
	return raw, nil
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func outputValueToString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func numericOrNow(v any) any {
	switch v.(type) {
	case float64, int, int64, json.Number:
		return v
	default:
		return time.Now().Unix()
	}
}
