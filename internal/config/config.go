// Package config loads the LLM connection settings and feature switches for the
// agent.
//
// Values come from a JSON file and can be overridden by environment variables;
// the CLI is responsible for merging those sources and applying flag overrides.
// JSON is used deliberately so the project stays dependency-free.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Built-in defaults, used when neither the config file nor the environment
// provides a value.
const (
	DefaultBaseURL        = "https://api.openai.com/v1"
	DefaultModel          = "gpt-4o-mini"
	DefaultMode           = "build"
	DefaultMaxSteps       = 12
	DefaultTimeoutSeconds = 300
	DefaultMemoryPath     = ".zcode/memory.md"
	DefaultRulesFile      = "AGENTS.md"
	DefaultSessionDir     = ".zcode/sessions"
	// DefaultBackupDir stores pre-write file snapshots used by /undo.
	DefaultBackupDir = ".zcode/backups"
	// DefaultBackupKeep bounds how many snapshots are retained; the oldest are
	// pruned first.
	DefaultBackupKeep = 100
	// DefaultCompactAt is the fraction of the context window at which history is
	// compacted.
	DefaultCompactAt = 0.75
	// DefaultKeepRecent is how many recent messages are always kept verbatim.
	DefaultKeepRecent = 6
	// DefaultPermission is the fallback tool-permission decision. "allow" keeps
	// the historical behavior; set it to "ask" to opt into approval prompts.
	DefaultPermission = "allow"
)

// Config holds the LLM connection settings and feature switches.
type Config struct {
	BaseURL        string            `json:"base_url"`
	APIKey         string            `json:"api_key"`
	Model          string            `json:"model"`
	MaxSteps       int               `json:"max_steps"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Mode           string            `json:"mode"`
	Modes          ModeConfig        `json:"modes"`
	Memory         MemoryConfig      `json:"memory"`
	Rules          RulesConfig       `json:"rules"`
	Session        SessionConfig     `json:"session"`
	Context        ContextConfig     `json:"context"`
	Backup         BackupConfig      `json:"backup"`
	Permissions    PermissionsConfig `json:"permissions"`
	MCP            MCPConfig         `json:"mcp"`
}

// RulesConfig configures project instruction files injected into the prompt.
type RulesConfig struct {
	Enabled *bool    `json:"enabled"`
	Files   []string `json:"files"`
}

// SessionConfig configures conversation persistence.
type SessionConfig struct {
	Enabled *bool  `json:"enabled"`
	Dir     string `json:"dir"`
}

// ContextConfig configures context-window compaction. MaxTokens is the model
// window; zero disables compaction.
type ContextConfig struct {
	MaxTokens  int     `json:"max_tokens"`
	CompactAt  float64 `json:"compact_at"`
	KeepRecent int     `json:"keep_recent"`
}

// BackupConfig configures pre-write file snapshots. Dir is relative to the
// project root unless absolute. Keep bounds how many snapshots are retained.
type BackupConfig struct {
	Enabled *bool  `json:"enabled"`
	Dir     string `json:"dir"`
	Keep    int    `json:"keep"`
}

// PermissionsConfig configures the tool-permission policy. Decisions are
// "allow", "ask", or "deny".
type PermissionsConfig struct {
	Default  string            `json:"default"`
	Tools    map[string]string `json:"tools"`
	Commands CommandPolicy     `json:"commands"`
}

// CommandPolicy lists run_command argv prefixes that are always allowed or
// denied, checked before the default decision.
type CommandPolicy struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// ModeConfig optionally overrides the built-in system prompt per mode.
type ModeConfig struct {
	Plan  ModeSettings `json:"plan"`
	Build ModeSettings `json:"build"`
}

// ModeSettings holds the optional per-mode system prompt override.
type ModeSettings struct {
	System string `json:"system"`
}

// MemoryConfig configures project memory.
type MemoryConfig struct {
	Enabled *bool  `json:"enabled"`
	Path    string `json:"path"`
}

// MCPConfig configures Model Context Protocol servers.
type MCPConfig struct {
	Enabled *bool       `json:"enabled"`
	Servers []MCPServer `json:"servers"`
}

// MCPServer describes one MCP server. Command/Args/Env are for the stdio
// transport.
type MCPServer struct {
	Name     string            `json:"name"`
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	ReadOnly bool              `json:"read_only"`
	Enabled  *bool             `json:"enabled"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		BaseURL:        DefaultBaseURL,
		Model:          DefaultModel,
		Mode:           DefaultMode,
		MaxSteps:       DefaultMaxSteps,
		TimeoutSeconds: DefaultTimeoutSeconds,
		Memory:         MemoryConfig{Path: DefaultMemoryPath},
		Rules:          RulesConfig{Files: []string{DefaultRulesFile}},
		Session:        SessionConfig{Dir: DefaultSessionDir},
		Context:        ContextConfig{CompactAt: DefaultCompactAt, KeepRecent: DefaultKeepRecent},
		Backup:         BackupConfig{Dir: DefaultBackupDir, Keep: DefaultBackupKeep},
		Permissions:    PermissionsConfig{Default: DefaultPermission},
	}
}

// Load reads path and overlays it on Default(), so fields omitted from the file
// keep their default value.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, nil
}

// ApplyEnv overrides fields with non-empty LLM_*/ZCODE_* environment variables.
func (c *Config) ApplyEnv() {
	if v := os.Getenv("LLM_BASE_URL"); v != "" {
		c.BaseURL = v
	}
	if v := os.Getenv("LLM_API_KEY"); v != "" {
		c.APIKey = v
	}
	if v := os.Getenv("LLM_MODEL"); v != "" {
		c.Model = v
	}
	if v := os.Getenv("ZCODE_MODE"); v != "" {
		c.Mode = v
	}
	if v, ok := os.LookupEnv("ZCODE_MEMORY"); ok {
		c.Memory.Enabled = boolPtr(parseBool(v))
	}
	if v, ok := os.LookupEnv("ZCODE_MCP"); ok {
		c.MCP.Enabled = boolPtr(parseBool(v))
	}
	if v, ok := os.LookupEnv("ZCODE_RULES"); ok {
		c.Rules.Enabled = boolPtr(parseBool(v))
	}
	if v, ok := os.LookupEnv("ZCODE_SESSION"); ok {
		c.Session.Enabled = boolPtr(parseBool(v))
	}
	if v, ok := os.LookupEnv("ZCODE_BACKUP"); ok {
		c.Backup.Enabled = boolPtr(parseBool(v))
	}
}

// Normalize trims a trailing slash from BaseURL, restores defaults for empty
// fields, and clamps invalid numbers and modes.
func (c *Config) Normalize() {
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = DefaultMaxSteps
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = DefaultTimeoutSeconds
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode != "plan" && c.Mode != "build" {
		c.Mode = DefaultMode
	}
	if strings.TrimSpace(c.Memory.Path) == "" {
		c.Memory.Path = DefaultMemoryPath
	}
	if len(c.Rules.Files) == 0 {
		c.Rules.Files = []string{DefaultRulesFile}
	}
	if strings.TrimSpace(c.Session.Dir) == "" {
		c.Session.Dir = DefaultSessionDir
	}
	if strings.TrimSpace(c.Backup.Dir) == "" {
		c.Backup.Dir = DefaultBackupDir
	}
	if c.Backup.Keep <= 0 {
		c.Backup.Keep = DefaultBackupKeep
	}
	if c.Context.CompactAt <= 0 || c.Context.CompactAt >= 1 {
		c.Context.CompactAt = DefaultCompactAt
	}
	if c.Context.KeepRecent < 1 {
		c.Context.KeepRecent = DefaultKeepRecent
	}
	c.Permissions.Default = normalizeDecision(c.Permissions.Default)
	if c.Permissions.Default == "" {
		c.Permissions.Default = DefaultPermission
	}
	for name, decision := range c.Permissions.Tools {
		c.Permissions.Tools[name] = normalizeDecision(decision)
	}
	for i := range c.MCP.Servers {
		s := &c.MCP.Servers[i]
		s.Name = strings.TrimSpace(s.Name)
		s.Command = strings.TrimSpace(s.Command)
	}
}

// MemoryOn reports whether project memory is enabled (default true).
func (c Config) MemoryOn() bool { return boolValue(c.Memory.Enabled, true) }

// RulesOn reports whether project instruction files are injected (default true).
func (c Config) RulesOn() bool { return boolValue(c.Rules.Enabled, true) }

// SessionOn reports whether conversation persistence is enabled (default true).
func (c Config) SessionOn() bool { return boolValue(c.Session.Enabled, true) }

// BackupOn reports whether pre-write snapshots are enabled (default true).
func (c Config) BackupOn() bool { return boolValue(c.Backup.Enabled, true) }

// MCPOn reports whether MCP is enabled (default false).
func (c Config) MCPOn() bool { return boolValue(c.MCP.Enabled, false) }

// normalizeDecision lower-cases a permission decision and drops unknown values.
func normalizeDecision(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow", "ask", "deny":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return ""
	}
}

// On reports whether this server is enabled (default true).
func (s MCPServer) On() bool { return boolValue(s.Enabled, true) }

func boolValue(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func boolPtr(b bool) *bool { return &b }

// parseBool treats the usual falsey strings as false and everything else as true.
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
