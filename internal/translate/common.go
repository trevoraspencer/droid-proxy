package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func copyIfPresent(dst, src map[string]any, keys ...string) {
	for _, k := range keys {
		if v, ok := src[k]; ok {
			dst[k] = v
		}
	}
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
