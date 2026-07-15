package config

// ParseForTest parses raw YAML config bytes using the same logic as Load,
// including presence tracking. It is intended for test helpers in other
// packages that need a Config with presence information without writing a
// file.
func ParseForTest(yamlBody string) (*Config, error) {
	return parse([]byte(yamlBody))
}
