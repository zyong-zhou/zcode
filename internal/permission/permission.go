// Package permission decides whether a tool call may run, based on a policy
// loaded from configuration. It is deliberately free of UI concerns: callers
// turn "ask" into a prompt (or a denial) themselves.
package permission

import (
	"encoding/json"
	"strings"
)

// Decision is the outcome for a tool call.
type Decision string

const (
	// Allow lets the call run.
	Allow Decision = "allow"
	// Ask defers to the caller (an interactive prompt, or a denial when there
	// is no way to ask).
	Ask Decision = "ask"
	// Deny blocks the call.
	Deny Decision = "deny"
)

// Policy resolves a decision for a tool call. The zero value allows everything.
type Policy struct {
	def   Decision
	tools map[string]Decision
	allow []string
	deny  []string
}

// New builds a policy. defaultDecision is "allow", "ask", or "deny"; unknown
// values fall back to "allow". tools maps a tool name to its decision. allow and
// deny are run_command argv prefixes checked before the default.
func New(defaultDecision string, tools map[string]string, allow, deny []string) Policy {
	p := Policy{
		def:   parse(defaultDecision),
		tools: map[string]Decision{},
		allow: clean(allow),
		deny:  clean(deny),
	}
	if p.def == "" {
		p.def = Allow
	}
	for name, d := range tools {
		if dd := parse(d); dd != "" {
			p.tools[name] = dd
		}
	}
	return p
}

// Decide returns the decision for a tool call. For run_command, the deny list is
// checked first, then the allow list, then the per-tool and default decisions.
func (p Policy) Decide(toolName, args string) Decision {
	if toolName == "run_command" {
		if cmd := commandLine(args); cmd != "" {
			for _, d := range p.deny {
				if matchPrefix(cmd, d) {
					return Deny
				}
			}
			for _, a := range p.allow {
				if matchPrefix(cmd, a) {
					return Allow
				}
			}
		}
	}
	if d, ok := p.tools[toolName]; ok {
		return d
	}
	if p.def == "" {
		return Allow
	}
	return p.def
}

func parse(s string) Decision {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow
	case "ask":
		return Ask
	case "deny":
		return Deny
	default:
		return ""
	}
}

// commandLine renders a run_command call as "command arg1 arg2" for matching.
func commandLine(args string) string {
	var in struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return ""
	}
	parts := append([]string{strings.TrimSpace(in.Command)}, in.Args...)
	return strings.TrimSpace(strings.Join(parts, " "))
}

// matchPrefix reports whether cmd equals pattern or starts with "pattern ".
func matchPrefix(cmd, pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	return cmd == pattern || strings.HasPrefix(cmd, pattern+" ")
}

func clean(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
