package app

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mapper.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigDefaultsWhenFileMissing(t *testing.T) {
	t.Setenv("MAPPER_CONFIG", "")
	// Default path ./cmd/mapper/mapper.yaml exists in the repo; point away explicitly.
	t.Chdir(t.TempDir())
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.Store != "./data/mapper" || cfg.LogDir != "./data/logs" || cfg.LogRetentionDays != 365 || cfg.RequireTLS {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadConfigFileAndEnvOverride(t *testing.T) {
	path := writeConfig(t, "# comment line\naddr: \"127.0.0.1:9090\" # trailing\nstore: /var/lib/mapper # x\nrequire_tls: yes\nlogging:\n  dir: '/var/log/mapper' # quoted\n  retention_days: 30\n")
	t.Setenv("MAPPER_CONFIG", path)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9090" || cfg.Store != "/var/lib/mapper" || !cfg.RequireTLS || cfg.LogDir != "/var/log/mapper" || cfg.LogRetentionDays != 30 {
		t.Fatalf("unexpected file values: %+v", cfg)
	}
	t.Setenv("MAPPER_ADDR", "127.0.0.1:1")
	t.Setenv("MAPPER_DB", "/tmp/db")
	t.Setenv("MAPPER_LOG_DIR", "/tmp/logs")
	t.Setenv("MAPPER_LOG_RETENTION_DAYS", "7")
	t.Setenv("MAPPER_REQUIRE_TLS", "0")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:1" || cfg.Store != "/tmp/db" || cfg.LogDir != "/tmp/logs" || cfg.LogRetentionDays != 7 || cfg.RequireTLS {
		t.Fatalf("env did not override: %+v", cfg)
	}
}

func TestLoadConfigRejectsBadInput(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":      "bogus: 1\n",
		"unknown nested":   "logging:\n  bogus: 1\n",
		"duplicate":        "addr: :1\naddr: :2\n",
		"tab indent":       "logging:\n\t dir: x\n",
		"bad indent":       "logging:\n    dir: x\n",
		"orphan indent":    "  dir: x\n",
		"no colon":         "just words\n",
		"bad bool":         "require_tls: sometimes\n",
		"retention zero":   "logging:\n  retention_days: 0\n",
		"retention huge":   "logging:\n  retention_days: 99999\n",
		"retention text":   "logging:\n  retention_days: forever\n",
		"unterminated":     "addr: \"abc\n",
		"trailing junk":    "addr: \"abc\" junk\n",
		"empty required":   "addr:\n",
		"nested has value": "logging: foo\n",
	} {
		path := writeConfig(t, body)
		t.Setenv("MAPPER_CONFIG", path)
		if _, err := loadConfig(); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
	t.Setenv("MAPPER_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	if _, err := loadConfig(); err == nil {
		t.Fatal("explicit missing MAPPER_CONFIG: expected error")
	}
	t.Setenv("MAPPER_CONFIG", writeConfig(t, "addr: :1\n"))
	t.Setenv("MAPPER_LOG_RETENTION_DAYS", "bogus")
	if _, err := loadConfig(); err == nil {
		t.Fatal("bad MAPPER_LOG_RETENTION_DAYS: expected error")
	}
}

func TestShippedMapperYAMLParses(t *testing.T) {
	// Tests run in the package dir; the shipped file lives at the repo root.
	t.Setenv("MAPPER_CONFIG", "../../cmd/mapper/mapper.yaml")
	for _, key := range []string{"MAPPER_ADDR", "MAPPER_DB", "MAPPER_TLS_CERT", "MAPPER_TLS_KEY", "MAPPER_REQUIRE_TLS", "MAPPER_CLIENT_CA", "MAPPER_LOG_DIR", "MAPPER_LOG_RETENTION_DAYS"} {
		t.Setenv(key, "")
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogRetentionDays != 365 || cfg.LogDir != "./data/logs" {
		t.Fatalf("shipped mapper.yaml unexpected: %+v", cfg)
	}
}
