package migration

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// RewriteListenPortScalar replaces the listen.port scalar value from oldPort
// to newPort in the raw YAML bytes, preserving every other byte exactly.
//
// portNode must be the yaml.Node for the listen.port value whose Value is the
// string representation of oldPort. The function locates the byte range of the
// old port digits at the node's line/column position and replaces only those
// digits. Quoting and tag decoration are preserved.
func RewriteListenPortScalar(raw []byte, portNode *yaml.Node, oldPort, newPort int) ([]byte, error) {
	if portNode == nil {
		return nil, fmt.Errorf("port node is nil")
	}
	oldStr := fmt.Sprintf("%d", oldPort)
	newStr := fmt.Sprintf("%d", newPort)

	// Convert 1-indexed line/column to 0-indexed byte offset.
	startOffset, err := nodeByteOffset(raw, portNode.Line, portNode.Column)
	if err != nil {
		return nil, fmt.Errorf("locate port scalar: %w", err)
	}

	// From the start offset, find the old port digit sequence.
	idx := strings.Index(string(raw[startOffset:]), oldStr)
	if idx < 0 {
		return nil, fmt.Errorf("port value %q not found at expected position (line %d, col %d); the scalar may use a non-decimal representation", oldStr, portNode.Line, portNode.Column)
	}

	replaceStart := startOffset + int64(idx)
	replaceEnd := replaceStart + int64(len(oldStr))

	// Verify that the replaced text is the port value and not part of a
	// larger token by checking the surrounding bytes are not digits.
	if replaceStart > 0 && isDigitByte(raw[replaceStart-1]) {
		return nil, fmt.Errorf("port value %q is preceded by a digit; ambiguous position", oldStr)
	}
	if int(replaceEnd) < len(raw) && isDigitByte(raw[replaceEnd]) {
		return nil, fmt.Errorf("port value %q is followed by a digit; ambiguous position", oldStr)
	}

	result := make([]byte, 0, len(raw)-len(oldStr)+len(newStr))
	result = append(result, raw[:replaceStart]...)
	result = append(result, []byte(newStr)...)
	result = append(result, raw[replaceEnd:]...)
	return result, nil
}

// nodeByteOffset converts a 1-indexed YAML line/column position into a 0-indexed
// byte offset in the raw document.
func nodeByteOffset(raw []byte, line, col int) (int64, error) {
	if line < 1 || col < 1 {
		return 0, fmt.Errorf("invalid line/column: %d/%d", line, col)
	}
	var offset int64
	currentLine := 1
	for _, b := range raw {
		if currentLine == line {
			targetCol := col - 1 // convert to 0-indexed
			if int(offset)+targetCol > len(raw) {
				return 0, fmt.Errorf("column %d exceeds line %d length", col, line)
			}
			return offset + int64(targetCol), nil
		}
		offset++
		if b == '\n' {
			currentLine++
		}
	}
	return 0, fmt.Errorf("line %d not found in document", line)
}

func isDigitByte(b byte) bool {
	return b >= '0' && b <= '9'
}
