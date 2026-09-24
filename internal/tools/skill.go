package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SkillInfo describes one loadable skill.
type SkillInfo struct {
	Name        string
	Description string
	Body        string
}

// SkillTool lets the model load a named skill's full instructions on demand.
// Keeping them behind a tool keeps the base system prompt small.
type SkillTool struct {
	Skills []SkillInfo
}

func (t SkillTool) Name() string { return "skill" }

// PlanSafe reports that loading instructions is allowed in plan mode.
func (t SkillTool) PlanSafe() bool { return true }

func (t SkillTool) Description() string {
	if len(t.Skills) == 0 {
		return "Load the full instructions for a skill by name."
	}
	var b strings.Builder
	b.WriteString("Load the full instructions for a skill by name. Available skills:")
	for _, s := range t.sorted() {
		b.WriteString("\n- " + s.Name)
		if s.Description != "" {
			b.WriteString(": " + s.Description)
		}
	}
	return b.String()
}

func (t SkillTool) Parameters() map[string]any {
	names := make([]string, 0, len(t.Skills))
	for _, s := range t.sorted() {
		names = append(names, s.Name)
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Skill name to load.",
				"enum":        names,
			},
		},
		"required": []string{"name"},
	}
}

func (t SkillTool) Run(_ context.Context, args string) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("skill: bad arguments: %w", err)
	}
	name := strings.TrimSpace(in.Name)
	for _, s := range t.Skills {
		if s.Name == name {
			return s.Body, nil
		}
	}
	return "", fmt.Errorf("skill: unknown skill %q", name)
}

// sorted returns a copy ordered by name so specs are stable across runs.
func (t SkillTool) sorted() []SkillInfo {
	out := append([]SkillInfo(nil), t.Skills...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
