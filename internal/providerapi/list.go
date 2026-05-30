// Package providerapi queries OpenAI-compatible providers for their available
// model IDs so the interactive config UI can offer a pick-list instead of
// requiring the user to paste a model slug. Callers fall back to free-text
// entry when discovery is unsupported or fails.
package providerapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ListModels performs GET {baseURL}/models and returns the discovered model
// IDs (sorted, de-duplicated). authHeader/authScheme default to
// "Authorization"/"Bearer"; pass an empty apiKey for no-auth upstreams.
func ListModels(ctx context.Context, baseURL, apiKey, authHeader, authScheme string) ([]string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("provider base_url is empty")
	}
	endpoint := base + "/models"

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) != "" {
		header := strings.TrimSpace(authHeader)
		if header == "" {
			header = "Authorization"
		}
		scheme := strings.TrimSpace(authScheme)
		if strings.EqualFold(header, "Authorization") && scheme == "" {
			scheme = "Bearer"
		}
		value := apiKey
		if scheme != "" {
			value = scheme + " " + apiKey
		}
		req.Header.Set(header, value)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider returned %s", resp.Status)
	}
	ids, err := parseModelIDs(body)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no models returned by provider")
	}
	return ids, nil
}

// parseModelIDs handles the common shapes: {"data":[{"id":...}]} (OpenAI),
// {"models":[{"id"|"name":...}]}, and a bare array of objects or strings.
func parseModelIDs(body []byte) ([]string, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, fmt.Errorf("empty response")
	}

	type modelObj struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	pick := func(objs []modelObj) []string {
		var out []string
		for _, o := range objs {
			if id := strings.TrimSpace(firstNonEmpty(o.ID, o.Name)); id != "" {
				out = append(out, id)
			}
		}
		return out
	}

	if strings.HasPrefix(trimmed, "{") {
		var wrapper struct {
			Data   []modelObj `json:"data"`
			Models []modelObj `json:"models"`
		}
		if err := json.Unmarshal(body, &wrapper); err == nil {
			if ids := pick(wrapper.Data); len(ids) > 0 {
				return dedupeSort(ids), nil
			}
			if ids := pick(wrapper.Models); len(ids) > 0 {
				return dedupeSort(ids), nil
			}
		}
		return nil, fmt.Errorf("unrecognized models response shape")
	}

	if strings.HasPrefix(trimmed, "[") {
		var objs []modelObj
		if err := json.Unmarshal(body, &objs); err == nil {
			if ids := pick(objs); len(ids) > 0 {
				return dedupeSort(ids), nil
			}
		}
		var strs []string
		if err := json.Unmarshal(body, &strs); err == nil {
			return dedupeSort(strs), nil
		}
	}
	return nil, fmt.Errorf("unrecognized models response shape")
}

func dedupeSort(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
