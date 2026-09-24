package mcp

import (
	"strings"
	"testing"
)

func TestAdapterMetadata(t *testing.T) {
	// An empty description and schema get sensible defaults.
	tool := NewTool("srv", nil, ToolInfo{Name: "do_thing"}, false)
	if name := tool.Name(); !strings.Contains(name, "do_thing") || !strings.Contains(name, "srv") {
		t.Errorf("Name() = %q, want namespaced name", name)
	}
	d := tool.Description()
	if !strings.Contains(d, "do_thing") || !strings.Contains(d, "srv") {
		t.Errorf("Description() = %q, want a default mentioning the tool and server", d)
	}
	if p := tool.Parameters(); p["type"] != "object" {
		t.Errorf("Parameters() = %+v, want an object schema", p)
	}
	if tool.PlanSafe() {
		t.Error("a non-read-only tool must not be plan-safe")
	}

	// Explicit description and schema pass through unchanged.
	schema := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}
	custom := NewTool("srv", nil, ToolInfo{Name: "x", Description: "自定义说明", InputSchema: schema}, false)
	if custom.Description() != "自定义说明" {
		t.Errorf("Description() = %q, want passthrough", custom.Description())
	}
	if got := custom.Parameters(); got["type"] != "object" || got["properties"] == nil {
		t.Errorf("Parameters() = %+v, want the server schema", got)
	}
}

func TestAdapterPlanSafety(t *testing.T) {
	if !NewTool("s", nil, ToolInfo{Name: "r"}, true).PlanSafe() {
		t.Error("a read-only server should make its tools plan-safe")
	}
	if !NewTool("s", nil, ToolInfo{Name: "r", ReadOnly: true}, false).PlanSafe() {
		t.Error("a tool marked ReadOnly should be plan-safe")
	}
}
