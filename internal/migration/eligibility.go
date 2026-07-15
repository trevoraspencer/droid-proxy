package migration

import (
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// eligibleLoopbackHosts are the only listen.host values that qualify a config
// for automatic or explicit migration. Configured `[::1]` (with brackets),
// wildcard binds, and all other hostnames/addresses are ineligible.
var eligibleLoopbackHosts = map[string]bool{
	"127.0.0.1": true,
	"localhost": true,
	"::1":       true,
}

// ConfigAnalysis describes whether a config file is eligible for migration and
// why. When Eligible is true, PortNode and DocNode are populated for the exact
// rewrite.
type ConfigAnalysis struct {
	Eligible  bool
	Reason    string
	Host      string
	Port      int
	OldPort   int
	NewPort   int
	PortNode  *yaml.Node // the listen.port value node
	DocNode   *yaml.Node // the root document node
	RawConfig []byte     // the raw config bytes (for rewrite)
}

// AnalyzeConfig reads and analyzes a config file for port migration
// eligibility. oldPort and newPort are the migration source and destination
// ports (normally 8787 and 9787).
func AnalyzeConfig(path string, oldPort, newPort int) (*ConfigAnalysis, error) {
	trust, err := CheckFileTrust(path)
	if err != nil {
		return nil, err
	}
	if !trust.Trusted {
		return &ConfigAnalysis{Eligible: false, Reason: trust.Reason}, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	return AnalyzeConfigBytes(raw, oldPort, newPort)
}

// AnalyzeConfigBytes analyzes raw YAML bytes for port migration eligibility.
// It is separated from AnalyzeConfig so tests can pass fixture bytes directly.
func AnalyzeConfigBytes(raw []byte, oldPort, newPort int) (*ConfigAnalysis, error) {
	analysis := &ConfigAnalysis{OldPort: oldPort, NewPort: newPort, RawConfig: raw}

	// Parse into a node tree for position and style information.
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		analysis.Reason = fmt.Sprintf("config is not valid YAML: %s", sanitizeYAMLError(err))
		return analysis, nil
	}

	// Check for multiple YAML documents using the parser, not naive text
	// matching. A "---" inside a block scalar (literal | or folded >) is
	// content, not a document separator; only the parser knows the
	// difference.
	if hasMultipleDocuments(raw) {
		analysis.Reason = "config contains multiple YAML documents; migration requires a single document"
		return analysis, nil
	}

	analysis.DocNode = &doc
	root := mappingNode(&doc)
	if root == nil {
		analysis.Reason = "config top-level is not a mapping"
		return analysis, nil
	}

	listenNode := findChild(root, "listen")
	if listenNode == nil || listenNode.Kind != yaml.MappingNode {
		// No listen block or listen is not a mapping. This is a no-op
		// (omitted port), not a refusal.
		analysis.Reason = "config has no listen mapping; no explicit port to migrate"
		return analysis, nil
	}

	portNode := findChild(listenNode, "port")
	if portNode == nil {
		// listen.port is omitted; this is a no-op.
		analysis.Reason = "listen.port is omitted; nothing to migrate"
		return analysis, nil
	}

	// Check the port scalar for alias, merge, anchor, or custom tag issues.
	if reason := checkPortScalarSafety(portNode); reason != "" {
		analysis.Reason = reason
		return analysis, nil
	}

	hostNode := findChild(listenNode, "host")
	host := ""
	if hostNode != nil {
		host = strings.TrimSpace(hostNode.Value)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	analysis.Host = host

	// Check that the host is an eligible loopback.
	// Configured [::1] (with brackets) is ineligible.
	if host == "[::1]" || strings.Contains(host, "[") || strings.Contains(host, "]") {
		analysis.Reason = fmt.Sprintf("listen.host %q contains brackets; use ::1 without brackets", host)
		return analysis, nil
	}
	if !eligibleLoopbackHosts[host] {
		analysis.Reason = fmt.Sprintf("listen.host %q is not an eligible loopback address (allowed: 127.0.0.1, localhost, ::1)", host)
		return analysis, nil
	}

	// Check the port value.
	portValue := portNode.Value
	portInt := parsePortValue(portValue)
	analysis.Port = portInt
	analysis.PortNode = portNode

	if portInt < 0 {
		analysis.Reason = fmt.Sprintf("listen.port value %q is not a decimal integer; migration requires a plain or quoted decimal scalar", portValue)
		return analysis, nil
	}

	// Verify the source bytes at the node position contain the decimal
	// representation. This rejects non-decimal encodings (hex, octal) whose
	// decoded value matches but whose source representation cannot be
	// byte-exactly rewritten.
	if !sourceHasDecimalPort(raw, portNode, portInt) {
		analysis.Reason = fmt.Sprintf("listen.port source representation is not a plain decimal %d; migration requires a decimal scalar", portInt)
		return analysis, nil
	}

	if portInt != oldPort {
		// Not the old default. This is a no-op, not an error.
		analysis.Reason = fmt.Sprintf("listen.port is %d, not the old default %d; nothing to migrate", portInt, oldPort)
		return analysis, nil
	}

	// Eligible!
	analysis.Eligible = true
	return analysis, nil
}

// checkPortScalarSafety inspects the port value node for YAML features that
// make an exact byte-level replacement unsafe: aliases, merges, anchors
// referenced elsewhere, and custom tags. Returns a non-empty reason string if
// the scalar is unsafe.
func checkPortScalarSafety(portNode *yaml.Node) string {
	if portNode.Kind == yaml.AliasNode {
		return "listen.port is an alias; migration requires a concrete scalar"
	}

	// Custom tag (anything other than the implicit !!int/!!str resolution).
	if portNode.Tag != "" && portNode.Tag != "!!int" && portNode.Tag != "!!str" {
		return fmt.Sprintf("listen.port uses a custom tag %q; migration requires a plain or quoted decimal scalar", portNode.Tag)
	}

	// Anchor on the port node. If the anchor is referenced elsewhere, changing
	// the value here would affect other nodes.
	if portNode.Anchor != "" {
		return fmt.Sprintf("listen.port carries an anchor %q that may be referenced elsewhere", portNode.Anchor)
	}

	return ""
}

// findChild returns the value node for a key in a mapping node, or nil.
func findChild(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// mappingNode returns the root mapping node from a document node.
func mappingNode(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	if doc.Kind == yaml.MappingNode {
		return doc
	}
	return nil
}

// parsePortValue parses a YAML port scalar string to an int. Returns -1 if the
// value is not a plain decimal integer.
func parsePortValue(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1
	}
	// Verify the entire string is digits (reject hex, octal, etc.).
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return -1
	}
	return n
}

// sourceHasDecimalPort checks that the raw bytes at the port node's position
// contain the decimal representation of the port. This rejects non-decimal
// encodings (hex, octal) whose decoded value matches but whose source
// representation cannot be byte-exactly rewritten. For quoted scalars the
// column points to the opening quote, so we look for the digits nearby.
func sourceHasDecimalPort(raw []byte, portNode *yaml.Node, port int) bool {
	offset, err := nodeByteOffset(raw, portNode.Line, portNode.Column)
	if err != nil {
		return false
	}
	portStr := fmt.Sprintf("%d", port)
	// For plain scalars, the port digits start at offset.
	if strings.HasPrefix(string(raw[offset:]), portStr) {
		return true
	}
	// For quoted scalars, the opening quote (or tag prefix) may precede the
	// digits. Scan forward up to 8 bytes for the decimal representation
	// preceded by a non-digit boundary.
	searchEnd := offset + 16
	if int(searchEnd) > len(raw) {
		searchEnd = int64(len(raw))
	}
	window := string(raw[offset:searchEnd])
	idx := strings.Index(window, portStr)
	if idx < 0 {
		return false
	}
	// The match must not be preceded by a digit (would be a longer number).
	checkPos := offset + int64(idx)
	if checkPos > 0 && isDigitByte(raw[checkPos-1]) {
		return false
	}
	return true
}

// hasMultipleDocuments checks whether the raw YAML contains more than one
// document. It uses a yaml.Decoder to count documents rather than naive text
// matching, so "---" inside block scalars (literal | or folded >) is correctly
// treated as content rather than a document separator.
func hasMultipleDocuments(raw []byte) bool {
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	count := 0
	for {
		var node yaml.Node
		if err := dec.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			// Parse error: let the earlier unmarshal handle reporting.
			break
		}
		count++
		if count > 1 {
			return true
		}
	}
	return count > 1
}

// sanitizeYAMLError strips any file path references from a YAML parse error.
func sanitizeYAMLError(err error) string {
	s := err.Error()
	// yaml.v3 errors sometimes include the input; we just return the message.
	return s
}
