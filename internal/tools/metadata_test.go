package tools

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// opaqueTool implements Tool but not PlanSafe, so it must be treated as unsafe.
type opaqueTool struct{}

func (opaqueTool) Name() string                                { return "opaque" }
func (opaqueTool) Description() string                         { return "no plan safety" }
func (opaqueTool) Parameters() map[string]any                  { return map[string]any{"type": "object"} }
func (opaqueTool) Run(context.Context, string) (string, error) { return "", nil }

func TestBuiltinToolMetadata(t *testing.T) {
	cases := []struct {
		tool     Tool
		wantName string
		planSafe bool
	}{
		{ListDir{Root: "."}, "list_dir", true},
		{ReadFile{Root: "."}, "read_file", true},
		{Search{Root: "."}, "search", true},
		{WriteFile{Root: "."}, "write_file", false},
		{Edit{Root: "."}, "edit", false},
		{RunCommand{Dir: "."}, "run_command", false},
		{Remember{}, "remember", true},
		{Recall{}, "recall", true},
		{Forget{}, "forget", true},
		{SkillTool{}, "skill", true},
	}
	for _, tc := range cases {
		if got := tc.tool.Name(); got != tc.wantName {
			t.Errorf("Name() = %q, want %q", got, tc.wantName)
		}
		if tc.tool.Description() == "" {
			t.Errorf("%s: empty Description", tc.wantName)
		}
		params := tc.tool.Parameters()
		if params == nil {
			t.Errorf("%s: nil Parameters", tc.wantName)
		} else if params["type"] != "object" {
			t.Errorf("%s: Parameters type = %v, want object", tc.wantName, params["type"])
		}
		if got := IsPlanSafe(tc.tool); got != tc.planSafe {
			t.Errorf("%s: IsPlanSafe = %v, want %v", tc.wantName, got, tc.planSafe)
		}
	}
}

func TestIsPlanSafeDefaultsFalse(t *testing.T) {
	if IsPlanSafe(opaqueTool{}) {
		t.Error("a tool without PlanSafe must be treated as unsafe")
	}
}

func TestTruncateShortPassesThrough(t *testing.T) {
	if got := truncate("hello", 0); got != "hello" {
		t.Errorf("truncate short = %q, want unchanged", got)
	}
	if got := Truncate("hello", 100); got != "hello" {
		t.Errorf("Truncate short = %q, want unchanged", got)
	}
}

func TestTruncateOmitsMiddle(t *testing.T) {
	long := strings.Repeat("a", 5000)
	got := truncate(long, 100)
	if !strings.Contains(got, "bytes omitted") {
		t.Fatalf("truncate did not insert an omission marker: %q", got)
	}
	if len(got) >= len(long) {
		t.Errorf("truncate result length = %d, want < %d", len(got), len(long))
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 10)) {
		t.Error("truncate should keep the head")
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	cn := strings.Repeat("中", 100)
	got := truncate(cn, 21)
	if !utf8.ValidString(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "bytes omitted") {
		t.Error("expected an omission marker")
	}
}
