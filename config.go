package main

// Mapper settings live in mapper.yaml (or the MAPPER_CONFIG path). The file
// uses a small strict YAML subset parsed below with no external dependency:
//
//   - top-level `key: value` scalars and `#` comments
//   - one nested map: `logging:` with 2-space indented `key: value` children
//   - double-quoted ("...") values with \\ \" \n \t escapes, single-quoted
//     ('...') literal values, or bare scalars
//   - booleans accept true/false/yes/no/on/off in any case
//
// Unknown keys, duplicate keys, tabs, and unexpected indentation are errors
// so a typo can never silently change what the server does. Environment
// variables override file values; an explicitly set MAPPER_CONFIG that cannot
// be read is an error, while a missing default ./mapper.yaml just means
// defaults.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Addr             string
	Store            string
	TLSCert          string
	TLSKey           string
	RequireTLS       bool
	ClientCA         string
	LogDir           string
	LogRetentionDays int
}

func defaultConfig() Config {
	return Config{
		Addr:             ":8080",
		Store:            "./data/mapper",
		LogDir:           "./data/logs",
		LogRetentionDays: 365,
	}
}

func configFilePath() string {
	if p := os.Getenv("MAPPER_CONFIG"); p != "" {
		return p
	}
	return "./mapper.yaml"
}

func loadConfig() (Config, error) {
	cfg := defaultConfig()
	path := configFilePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) || os.Getenv("MAPPER_CONFIG") != "" {
			return cfg, fmt.Errorf("read config: %w", err)
		}
	} else if err := parseConfigYAML(string(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if v := os.Getenv("MAPPER_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv("MAPPER_DB"); v != "" {
		cfg.Store = v
	}
	if v := os.Getenv("MAPPER_TLS_CERT"); v != "" {
		cfg.TLSCert = v
	}
	if v := os.Getenv("MAPPER_TLS_KEY"); v != "" {
		cfg.TLSKey = v
	}
	if v := os.Getenv("MAPPER_REQUIRE_TLS"); v != "" {
		cfg.RequireTLS = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	if v := os.Getenv("MAPPER_CLIENT_CA"); v != "" {
		cfg.ClientCA = v
	}
	if v := os.Getenv("MAPPER_LOG_DIR"); v != "" {
		cfg.LogDir = v
	}
	if v := os.Getenv("MAPPER_LOG_RETENTION_DAYS"); v != "" {
		n, err := parseRetentionDays(v)
		if err != nil {
			return cfg, fmt.Errorf("MAPPER_LOG_RETENTION_DAYS: %w", err)
		}
		cfg.LogRetentionDays = n
	}
	return cfg, nil
}

func parseRetentionDays(raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > 3650 {
		return 0, fmt.Errorf("retention must be 1..3650 days, got %q", raw)
	}
	return n, nil
}

func parseConfigBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "yes", "on":
		return true, nil
	case "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("expected a boolean, got %q", raw)
}

// stripComment cuts a trailing `# comment`, ignoring `#` inside quotes.
func stripComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			if c == '\\' && quote == '"' {
				i++
				continue
			}
			if c == quote {
				if quote == '\'' && i+1 < len(line) && line[i+1] == '\'' {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '#' {
			return line[:i]
		}
	}
	return line
}

func unquoteScalar(raw string, line int) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || (raw[0] != '"' && raw[0] != '\'') {
		return raw, nil
	}
	quote := raw[0]
	var out strings.Builder
	closed := false
	for i := 1; i < len(raw); i++ {
		c := raw[i]
		if quote == '"' && c == '\\' && i+1 < len(raw) {
			i++
			switch raw[i] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case '\\', '"':
				out.WriteByte(raw[i])
			default:
				return "", fmt.Errorf("line %d: unsupported escape %q", line, `\`+string(raw[i]))
			}
			continue
		}
		if c == quote {
			if quote == '\'' && i+1 < len(raw) && raw[i+1] == '\'' {
				out.WriteByte('\'')
				i++
				continue
			}
			closed = true
			if rest := strings.TrimSpace(raw[i+1:]); rest != "" {
				return "", fmt.Errorf("line %d: unexpected text after quoted value", line)
			}
			break
		}
		out.WriteByte(c)
	}
	if !closed {
		return "", fmt.Errorf("line %d: unterminated quoted value", line)
	}
	return out.String(), nil
}

func parseConfigYAML(text string, cfg *Config) error {
	seen := map[string]bool{}
	section := ""
	for num, line := range strings.Split(text, "\n") {
		lineNo := num + 1
		if strings.ContainsRune(line, '\t') {
			return fmt.Errorf("line %d: tabs are not allowed, use spaces", lineNo)
		}
		trimmed := strings.TrimSpace(stripComment(line))
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent != 0 && indent != 2 {
			return fmt.Errorf("line %d: indent with 0 or 2 spaces", lineNo)
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return fmt.Errorf("line %d: expected `key: value`", lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t\"'") {
			return fmt.Errorf("line %d: invalid key %q", lineNo, key)
		}
		if indent == 0 {
			section = ""
			if value == "" && key == "logging" {
				section = "logging"
				continue
			}
		} else if section == "" {
			return fmt.Errorf("line %d: indented keys belong under `logging:`", lineNo)
		}
		full := key
		if section != "" {
			full = section + "." + key
		}
		if seen[full] {
			return fmt.Errorf("line %d: duplicate key %q", lineNo, full)
		}
		seen[full] = true
		scalar, err := unquoteScalar(strings.TrimSpace(value), lineNo)
		if err != nil {
			return err
		}
		if err := applyConfigScalar(cfg, full, scalar, lineNo); err != nil {
			return err
		}
	}
	return nil
}

func applyConfigScalar(cfg *Config, key, value string, line int) error {
	switch key {
	case "map_style":
		// Accepted and ignored for compatibility with older configuration files.
	case "addr":
		cfg.Addr = value
	case "store":
		cfg.Store = value
	case "tls_cert":
		cfg.TLSCert = value
	case "tls_key":
		cfg.TLSKey = value
	case "client_ca":
		cfg.ClientCA = value
	case "require_tls":
		b, err := parseConfigBool(value)
		if err != nil {
			return fmt.Errorf("line %d: require_tls: %w", line, err)
		}
		cfg.RequireTLS = b
	case "logging.dir":
		cfg.LogDir = value
	case "logging.retention_days":
		n, err := parseRetentionDays(value)
		if err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		cfg.LogRetentionDays = n
	default:
		return fmt.Errorf("line %d: unknown setting %q", line, key)
	}
	if (key == "addr" || key == "store" || key == "logging.dir") && value == "" {
		return fmt.Errorf("line %d: %s must not be empty", line, key)
	}
	return nil
}
