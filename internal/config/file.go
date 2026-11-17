package config

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// secretKeys are the settings that must not sit in a file other users can read.
var secretKeys = []string{"SECRET_KEY", "ADMIN_PASSWORD"}

// readFile loads a KEY=VALUE file and refuses it when it holds secrets but is
// readable by group or others. Failing is deliberate (as ssh does for private
// keys): SECRET_KEY decrypts stored relay passwords and signs sessions, and a
// warning in a log is easy to miss. Container secret mounts can set the mode
// (Kubernetes defaultMode: 0400, Docker secrets mode: 0400).
func readFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is operator-provided configuration
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	vals, err := parseFile(string(data))
	if err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}
	if runtime.GOOS == "windows" {
		return vals, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat config file: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		for _, k := range secretKeys {
			if _, ok := vals[k]; ok {
				return nil, fmt.Errorf("config file %s holds %s but is readable by other users (mode %04o), run: chmod 600 %s",
					path, k, perm, path)
			}
		}
	}
	return vals, nil
}

// parseFile parses KEY=VALUE lines.
//
//   - Blank lines and lines starting with # are ignored, an optional
//     "export " prefix is accepted.
//   - A value may be wrapped in single or double quotes. Quotes are taken
//     literally, without escape sequences, and only a comment may follow the
//     closing quote. An unterminated quote is an error.
//   - In an unquoted value, a # at its start or preceded by a space or tab
//     starts a comment. A # inside a word (pass#word) is kept; quote values
//     that start with #.
func parseFile(data string) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t\"'") {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", i+1)
		}
		v, err := parseValue(strings.TrimSpace(val))
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", i+1, key, err)
		}
		out[key] = v
	}
	return out, nil
}

var errTrailing = errors.New("only a # comment may follow a quoted value")

func parseValue(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if q := v[0]; q == '"' || q == '\'' {
		end := strings.IndexByte(v[1:], q)
		if end < 0 {
			return "", fmt.Errorf("unterminated %c quote", q)
		}
		rest := strings.TrimSpace(v[end+2:])
		if rest != "" && !strings.HasPrefix(rest, "#") {
			return "", errTrailing
		}
		return v[1 : end+1], nil
	}
	if v[0] == '#' {
		return "", nil
	}
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimSpace(v[:i]), nil
		}
	}
	return v, nil
}
