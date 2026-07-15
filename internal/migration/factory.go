package migration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

// FactoryEntryChange describes one eligible Factory customModels entry whose
// baseUrl should be updated from OldOrigin to NewOrigin.
type FactoryEntryChange struct {
	Index      int    // index in the customModels array
	Model      string // the model alias
	OldOrigin  string // e.g. http://127.0.0.1:8787
	NewOrigin  string // e.g. http://127.0.0.1:9787
	ValueStart int64  // byte offset of the baseUrl value in the original document
	ValueEnd   int64  // byte offset after the baseUrl value
}

// FactoryAnalysis describes whether Factory entries are eligible for migration.
type FactoryAnalysis struct {
	Present    bool // Factory file exists
	HasEntries bool // customModels exists and is a non-empty array
	Changes    []FactoryEntryChange
	Duplicates []string // duplicate key paths detected
	Unsafe     bool     // malformed or otherwise unsafe JSON
	Reason     string   // sanitized reason when Unsafe or no changes
	Noop       bool     // present but no eligible entries (not an error)
}

// FactoryState describes the presence and safety of the Factory settings file.
type FactoryState int

const (
	FactoryAbsent FactoryState = iota
	FactoryEmpty
	FactorySafe
	FactoryUnsafe
)

// CheckFactoryState determines whether the Factory file at path is present and
// safe to analyze. An absent, empty, or whitespace-only file is treated as no
// dependent state (config-only migration). A malformed, unreadable, null,
// non-array customModels, or duplicate-member document is unsafe and aborts.
func CheckFactoryState(path string) (FactoryState, error) {
	raw, err := readFactoryFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FactoryAbsent, nil
		}
		return FactoryUnsafe, nil
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return FactoryEmpty, nil
	}
	// Check for valid top-level JSON object.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return FactoryUnsafe, nil
	}
	// Detect duplicate keys anywhere in the document.
	dupes, err := detectJSONDuplicates(raw)
	if err != nil || len(dupes) > 0 {
		return FactoryUnsafe, nil
	}
	// If customModels exists, it must be an array or null/absent.
	if cm, ok := root["customModels"]; ok {
		if len(cm) == 0 || string(cm) == "null" {
			return FactorySafe, nil
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(cm, &entries); err != nil {
			return FactoryUnsafe, nil
		}
	}
	return FactorySafe, nil
}

// AnalyzeFactory analyzes raw Factory settings JSON for port migration
// eligibility. models is the set of configured droid-proxy models for
// alias/provider matching. listenHost is the configured listen host.
// oldPort and newPort are the migration source/destination ports.
func AnalyzeFactory(raw []byte, models []*config.Model, listenHost string, oldPort, newPort int) (*FactoryAnalysis, error) {
	analysis := &FactoryAnalysis{Present: true}

	if len(strings.TrimSpace(string(raw))) == 0 {
		// Empty/whitespace file: config-only migration.
		analysis.Noop = true
		return analysis, nil
	}

	// Detect duplicate keys anywhere in the document before any selection.
	dupes, err := detectJSONDuplicates(raw)
	if err != nil {
		analysis.Unsafe = true
		analysis.Reason = "Factory settings JSON is malformed and cannot be analyzed"
		return analysis, nil
	}
	if len(dupes) > 0 {
		analysis.Unsafe = true
		analysis.Duplicates = dupes
		analysis.Reason = fmt.Sprintf("Factory settings JSON has duplicate member names: %s", strings.Join(dupes, ", "))
		return analysis, nil
	}

	// Parse top-level.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		analysis.Unsafe = true
		analysis.Reason = "Factory settings JSON is malformed and cannot be analyzed"
		return analysis, nil
	}

	cmRaw, hasCM := root["customModels"]
	if !hasCM || len(cmRaw) == 0 || string(cmRaw) == "null" {
		// No customModels: config-only migration.
		analysis.Noop = true
		return analysis, nil
	}

	// Parse customModels array.
	var entryRaws []json.RawMessage
	if err := json.Unmarshal(cmRaw, &entryRaws); err != nil {
		analysis.Unsafe = true
		analysis.Reason = "Factory customModels is not a JSON array"
		return analysis, nil
	}

	if len(entryRaws) == 0 {
		// Empty customModels: config-only migration.
		analysis.Noop = true
		return analysis, nil
	}

	analysis.HasEntries = true

	// Build alias/provider lookup from configured models.
	type modelKey struct {
		model    string
		provider string
	}
	known := make(map[modelKey]struct{}, len(models))
	for _, m := range models {
		if m == nil || strings.TrimSpace(m.Alias) == "" {
			continue
		}
		known[modelKey{model: m.Alias, provider: string(m.FactoryProvider)}] = struct{}{}
	}

	oldOrigin := config.FormatListenURL(listenHost, oldPort)
	newOrigin := config.FormatListenURL(listenHost, newPort)

	// Find eligible entries and their byte positions.
	searchFrom := 0
	for i, entryRaw := range entryRaws {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entryRaw, &fields); err != nil {
			analysis.Unsafe = true
			analysis.Reason = fmt.Sprintf("Factory customModels entry %d is malformed", i)
			return analysis, nil
		}

		modelName := jsonStringValue(fields["model"])
		provider := jsonStringValue(fields["provider"])
		baseURL := jsonStringValue(fields["baseUrl"])

		// Check fingerprint: model + provider + baseUrl must all match.
		if modelName == "" || provider == "" || baseURL == "" {
			continue
		}
		if _, isKnown := known[modelKey{model: modelName, provider: provider}]; !isKnown {
			continue
		}
		if baseURL != oldOrigin {
			continue
		}

		// Find the byte position of the baseUrl value within the original document.
		entryOffset := findBytesOffset(raw, entryRaw, searchFrom)
		if entryOffset < 0 {
			// Should not happen since entryRaw is a slice of raw.
			analysis.Unsafe = true
			analysis.Reason = fmt.Sprintf("cannot locate Factory customModels entry %d in document", i)
			return analysis, nil
		}
		searchFrom = entryOffset + len(entryRaw)

		// Find the baseUrl value position within the entry.
		baseURLValueStart, baseURLValueEnd, ok := findFieldValueRange(entryRaw, "baseUrl")
		if !ok {
			analysis.Unsafe = true
			analysis.Reason = fmt.Sprintf("cannot locate baseUrl value in Factory customModels entry %d", i)
			return analysis, nil
		}

		analysis.Changes = append(analysis.Changes, FactoryEntryChange{
			Index:      i,
			Model:      modelName,
			OldOrigin:  oldOrigin,
			NewOrigin:  newOrigin,
			ValueStart: int64(entryOffset) + int64(baseURLValueStart),
			ValueEnd:   int64(entryOffset) + int64(baseURLValueEnd),
		})
	}

	if len(analysis.Changes) == 0 {
		analysis.Noop = true
	}

	return analysis, nil
}

// RewriteFactory applies the eligible entry changes to the raw Factory JSON
// bytes, preserving all other bytes exactly. Only the port digits within each
// eligible baseUrl value are replaced.
func RewriteFactory(raw []byte, changes []FactoryEntryChange) ([]byte, error) {
	if len(changes) == 0 {
		return raw, nil
	}

	// Sort changes by ValueStart descending so we replace from right to left.
	sorted := make([]FactoryEntryChange, len(changes))
	copy(sorted, changes)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].ValueStart > sorted[i].ValueStart {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	result := raw
	for _, ch := range sorted {
		// The baseUrl value is a JSON string like "http://127.0.0.1:8787".
		// Within [ValueStart, ValueEnd), find the old port and replace it.
		valueBytes := result[ch.ValueStart:ch.ValueEnd]
		oldPortStr := portSuffix(ch.OldOrigin)
		newPortStr := portSuffix(ch.NewOrigin)

		idx := bytes.LastIndex(valueBytes, []byte(oldPortStr))
		if idx < 0 {
			return nil, fmt.Errorf("old port %q not found in baseUrl value", oldPortStr)
		}

		replaceStart := ch.ValueStart + int64(idx)
		replaceEnd := replaceStart + int64(len(oldPortStr))

		newResult := make([]byte, 0, len(result)-len(oldPortStr)+len(newPortStr))
		newResult = append(newResult, result[:replaceStart]...)
		newResult = append(newResult, []byte(newPortStr)...)
		newResult = append(newResult, result[replaceEnd:]...)
		result = newResult
	}

	return result, nil
}

// portSuffix extracts the port digits from an origin URL like
// "http://127.0.0.1:8787" → "8787".
func portSuffix(origin string) string {
	idx := strings.LastIndex(origin, ":")
	if idx < 0 {
		return ""
	}
	return origin[idx+1:]
}

// findBytesOffset finds the offset of sub within doc starting from searchFrom.
func findBytesOffset(doc, sub []byte, searchFrom int) int {
	if searchFrom > len(doc) {
		return -1
	}
	idx := bytes.Index(doc[searchFrom:], sub)
	if idx < 0 {
		return -1
	}
	return searchFrom + idx
}

// findFieldValueRange finds the byte range [start, end) of a JSON object field's
// value within the object's raw bytes. The range includes the full raw value
// (e.g., including quotes for a string).
func findFieldValueRange(objRaw []byte, fieldName string) (int, int, bool) {
	dec := json.NewDecoder(bytes.NewReader(objRaw))
	// Read opening {
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return 0, 0, false
	}
	for dec.More() {
		// Read key.
		keyToken, err := dec.Token()
		if err != nil {
			return 0, 0, false
		}
		key, ok := keyToken.(string)
		if !ok {
			return 0, 0, false
		}
		// Position after key token (after closing quote).
		afterKey := int(dec.InputOffset())
		// Skip whitespace and colon to find the value start.
		valueStart := skipJSONWhitespaceColon(objRaw, afterKey)
		// Read value token(s).
		if err := skipJSONValue(dec); err != nil {
			return 0, 0, false
		}
		valueEnd := int(dec.InputOffset())
		if key == fieldName {
			return valueStart, valueEnd, true
		}
	}
	return 0, 0, false
}

// skipJSONWhitespaceColon advances past whitespace and a single colon.
func skipJSONWhitespaceColon(raw []byte, from int) int {
	i := from
	colonSeen := false
	for i < len(raw) {
		c := raw[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if c == ':' && !colonSeen {
			colonSeen = true
			i++
			continue
		}
		break
	}
	return i
}

// skipJSONValue skips the next JSON value in the decoder (scalar, object, or
// array).
func skipJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); ok {
		if delim == '{' || delim == '[' {
			// Consume the rest of this object/array.
			for dec.More() {
				if err := skipJSONValue(dec); err != nil {
					return err
				}
			}
			// Read closing delim.
			if _, err := dec.Token(); err != nil {
				return err
			}
		}
	}
	return nil
}

// detectJSONDuplicates walks the entire JSON document and detects duplicate
// member names at every object level. Returns a list of JSON path strings for
// duplicate keys.
func detectJSONDuplicates(raw []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Check that the document is not empty or a bare scalar.
	if !dec.More() {
		return nil, nil
	}
	var dupes []string
	if err := detectDupesRecursive(dec, "$", &dupes); err != nil {
		return nil, err
	}
	return dupes, nil
}

func detectDupesRecursive(dec *json.Decoder, path string, dupes *[]string) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil // scalar, no duplicates possible
	}
	switch delim {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("expected string key")
			}
			keyPath := path + "." + key
			if seen[key] {
				*dupes = append(*dupes, keyPath)
			}
			seen[key] = true
			if err := detectDupesRecursive(dec, keyPath, dupes); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil { // closing }
			return err
		}
	case json.Delim('['):
		idx := 0
		for dec.More() {
			if err := detectDupesRecursive(dec, fmt.Sprintf("%s[%d]", path, idx), dupes); err != nil {
				return err
			}
			idx++
		}
		if _, err := dec.Token(); err != nil { // closing ]
			return err
		}
	}
	return nil
}

// jsonStringValue extracts a string from a json.RawMessage. Returns "" if the
// raw message is nil, empty, or not a string.
func jsonStringValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// readFactoryFile reads the Factory settings file, handling missing files.
func readFactoryFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
