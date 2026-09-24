package zcode

import (
	"strings"
	"testing"

	"github.com/zhouyong/zcode/internal/llm"
	"github.com/zhouyong/zcode/internal/tools"
)

func TestPromptPrefersPlanSystem(t *testing.T) {
	a := New(nil, tools.NewRegistry(), Options{System: "build-sys", PlanSystem: "plan-sys"})

	a.Mode = ModeBuild
	if got := a.prompt(); got != "build-sys" {
		t.Errorf("build prompt = %q, want build-sys", got)
	}

	a.Mode = ModePlan
	if got := a.prompt(); got != "plan-sys" {
		t.Errorf("plan prompt = %q, want plan-sys", got)
	}

	// Plan mode falls back to the base prompt when no plan prompt is set.
	a.PlanSystem = ""
	if got := a.prompt(); got != "build-sys" {
		t.Errorf("plan fallback prompt = %q, want build-sys", got)
	}
}

func TestPreviewFor(t *testing.T) {
	a := New(nil, tools.NewRegistry(), Options{})
	if got := a.previewFor(llm.ToolCall{Name: "run_command", Arguments: `{}`}); got != "" {
		t.Errorf("previewFor with no tools = %q, want empty", got)
	}

	// A nil registry must be safe too.
	a.Tools = nil
	if got := a.previewFor(llm.ToolCall{Name: "run_command"}); got != "" {
		t.Errorf("previewFor with nil tools = %q, want empty", got)
	}

	// A registered tool with a preview returns it; an unknown tool is empty.
	a.Tools = tools.NewRegistry(tools.RunCommand{})
	if got := a.previewFor(llm.ToolCall{Name: "run_command", Arguments: `{"command":"go","args":["build"]}`}); !strings.Contains(got, "go build") {
		t.Errorf("previewFor = %q, want the command line", got)
	}
	if got := a.previewFor(llm.ToolCall{Name: "missing"}); got != "" {
		t.Errorf("previewFor unknown tool = %q, want empty", got)
	}
}
