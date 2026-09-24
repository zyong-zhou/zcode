package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadOverlaysDefaults(t *testing.T) {
	path := writeFile(t, `{"model":"my-model"}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "my-model" {
		t.Errorf("model = %q", cfg.Model)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Errorf("base_url = %q, want default %q", cfg.BaseURL, DefaultBaseURL)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "http://localhost:11434/v1")
	t.Setenv("LLM_API_KEY", "secret")
	t.Setenv("LLM_MODEL", "env-model")

	cfg := Default()
	cfg.ApplyEnv()
	if cfg.BaseURL != "http://localhost:11434/v1" || cfg.APIKey != "secret" || cfg.Model != "env-model" {
		t.Fatalf("env override failed: %+v", cfg)
	}
}

func TestNormalize(t *testing.T) {
	cfg := Config{BaseURL: "http://x/v1/", Model: ""}
	cfg.Normalize()
	if cfg.BaseURL != "http://x/v1" {
		t.Errorf("base_url = %q", cfg.BaseURL)
	}
	if cfg.Model != DefaultModel {
		t.Errorf("model = %q, want default %q", cfg.Model, DefaultModel)
	}
}

func TestBackupKeepDefaults(t *testing.T) {
	if Default().Backup.Keep != DefaultBackupKeep {
		t.Errorf("default backup.keep = %d, want %d", Default().Backup.Keep, DefaultBackupKeep)
	}
	cfg := Config{Backup: BackupConfig{Keep: 0}}
	cfg.Normalize()
	if cfg.Backup.Keep != DefaultBackupKeep {
		t.Errorf("normalized backup.keep = %d, want %d", cfg.Backup.Keep, DefaultBackupKeep)
	}
	cfg = Config{Backup: BackupConfig{Keep: 5}}
	cfg.Normalize()
	if cfg.Backup.Keep != 5 {
		t.Errorf("explicit backup.keep = %d, want 5", cfg.Backup.Keep)
	}
}

func TestNormalizeFeatureDefaults(t *testing.T) {
	cfg := Config{}
	cfg.Normalize()
	if cfg.Mode != DefaultMode {
		t.Errorf("mode = %q, want %q", cfg.Mode, DefaultMode)
	}
	if cfg.MaxSteps != DefaultMaxSteps {
		t.Errorf("max_steps = %d, want %d", cfg.MaxSteps, DefaultMaxSteps)
	}
	if cfg.TimeoutSeconds != DefaultTimeoutSeconds {
		t.Errorf("timeout_seconds = %d, want %d", cfg.TimeoutSeconds, DefaultTimeoutSeconds)
	}
	if cfg.Memory.Path != DefaultMemoryPath {
		t.Errorf("memory path = %q, want %q", cfg.Memory.Path, DefaultMemoryPath)
	}
	if !cfg.MemoryOn() {
		t.Error("memory should default to enabled")
	}
	if cfg.MCPOn() {
		t.Error("mcp should default to disabled")
	}
}

func TestNormalizeNewSections(t *testing.T) {
	cfg := Config{}
	cfg.Normalize()
	if !cfg.RulesOn() || len(cfg.Rules.Files) != 1 || cfg.Rules.Files[0] != DefaultRulesFile {
		t.Errorf("rules = %+v", cfg.Rules)
	}
	if !cfg.SessionOn() || cfg.Session.Dir != DefaultSessionDir {
		t.Errorf("session = %+v", cfg.Session)
	}
	if cfg.Context.CompactAt != DefaultCompactAt || cfg.Context.KeepRecent != DefaultKeepRecent {
		t.Errorf("context = %+v", cfg.Context)
	}
	if !cfg.BackupOn() || cfg.Backup.Dir != DefaultBackupDir {
		t.Errorf("backup = %+v", cfg.Backup)
	}
	if cfg.Permissions.Default != DefaultPermission {
		t.Errorf("permissions = %+v", cfg.Permissions)
	}
}

func TestBackupEnvSwitch(t *testing.T) {
	t.Setenv("ZCODE_BACKUP", "0")
	cfg := Config{}
	cfg.ApplyEnv()
	if cfg.BackupOn() {
		t.Error("ZCODE_BACKUP=0 should disable backups")
	}
}

func TestPermissionDecisionNormalized(t *testing.T) {
	path := writeFile(t, `{"permissions":{"default":"ASK","tools":{"run_command":"Deny","edit":"bogus"}}}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Normalize()
	if cfg.Permissions.Default != "ask" {
		t.Errorf("default = %q, want ask", cfg.Permissions.Default)
	}
	if cfg.Permissions.Tools["run_command"] != "deny" {
		t.Errorf("run_command = %q, want deny", cfg.Permissions.Tools["run_command"])
	}
	if cfg.Permissions.Tools["edit"] != "" {
		t.Errorf("unknown decision should be dropped: %q", cfg.Permissions.Tools["edit"])
	}
}

func TestEnvFeatureSwitches(t *testing.T) {
	t.Setenv("ZCODE_MEMORY", "0")
	t.Setenv("ZCODE_MCP", "1")
	t.Setenv("ZCODE_MODE", "plan")

	cfg := Default()
	cfg.ApplyEnv()
	if cfg.MemoryOn() {
		t.Error("ZCODE_MEMORY=0 should disable memory")
	}
	if !cfg.MCPOn() {
		t.Error("ZCODE_MCP=1 should enable mcp")
	}
	if cfg.Mode != "plan" {
		t.Errorf("mode = %q, want plan", cfg.Mode)
	}
}

func TestLoadFeatureSection(t *testing.T) {
	path := writeFile(t, `{
		"max_steps": 5,
		"mode": "plan",
		"memory": {"enabled": false},
		"mcp": {"enabled": true, "servers": [{"name":"fs","command":"node"}]}
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxSteps != 5 || cfg.Mode != "plan" {
		t.Errorf("scalars = %+v", cfg)
	}
	if cfg.MemoryOn() {
		t.Error("memory should be disabled from the file")
	}
	if !cfg.MCPOn() || len(cfg.MCP.Servers) != 1 || cfg.MCP.Servers[0].Name != "fs" {
		t.Errorf("mcp = %+v", cfg.MCP)
	}
	if !cfg.MCP.Servers[0].On() {
		t.Error("server should default to enabled")
	}
}
