package zcode

import "strings"

// Mode selects the agent's capabilities and system prompt.
//
// Plan mode hides every tool that is not explicitly plan-safe, so the model
// cannot change the project; build mode exposes all tools.
type Mode string

const (
	// ModePlan is read-only: investigate and produce a plan.
	ModePlan Mode = "plan"
	// ModeBuild is the default: read, write, and run commands.
	ModeBuild Mode = "build"
)

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool { return m == ModePlan || m == ModeBuild }

// ParseMode normalizes a mode string, defaulting to ModeBuild.
func ParseMode(s string) Mode {
	m := Mode(strings.ToLower(strings.TrimSpace(s)))
	if m.Valid() {
		return m
	}
	return ModeBuild
}
