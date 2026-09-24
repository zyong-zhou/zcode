package tools

import (
	"context"
	"strings"
	"testing"
)

func TestSkillTool(t *testing.T) {
	tool := SkillTool{Skills: []SkillInfo{
		{Name: "release", Description: "发布流程", Body: "步骤一"},
		{Name: "design", Description: "设计流程", Body: "步骤二"},
	}}

	if got, err := tool.Run(context.Background(), `{"name":"release"}`); err != nil || got != "步骤一" {
		t.Errorf("Run = %q, %v", got, err)
	}
	if _, err := tool.Run(context.Background(), `{"name":"nope"}`); err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Errorf("unknown skill err = %v", err)
	}
	if _, err := tool.Run(context.Background(), `{`); err == nil {
		t.Error("bad arguments should fail")
	}
	if !IsPlanSafe(tool) {
		t.Error("skill tool should be plan-safe")
	}

	desc := tool.Description()
	if !strings.Contains(desc, "release") || !strings.Contains(desc, "发布流程") {
		t.Errorf("description = %q", desc)
	}
	// The enum is sorted so the spec is deterministic.
	params := tool.Parameters()
	props := params["properties"].(map[string]any)
	enum := props["name"].(map[string]any)["enum"].([]string)
	if len(enum) != 2 || enum[0] != "design" || enum[1] != "release" {
		t.Errorf("enum = %v", enum)
	}
}
