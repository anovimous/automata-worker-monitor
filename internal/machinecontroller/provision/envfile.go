package provision

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var envKeyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// ParseEnvFile reads KEY=VALUE lines. Blank lines and # comments are skipped, an "export " prefix is allowed, a value
// may be quoted with ' or "; nothing is expanded. An error names the line, never its value (the file holds the
// worker's credentials).
func ParseEnvFile(r io.Reader) (map[string]string, error) {
	env := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("line %d is not KEY=VALUE with a key like %s", n, envKeyPattern)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		env[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("failed to read the env file: %w", err)
	}
	return env, nil
}
