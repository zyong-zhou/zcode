package zcode_test

import (
	"testing"

	"github.com/zyong-zhou/zcode/internal/llm"
	"github.com/zyong-zhou/zcode/internal/tools"
	"github.com/zyong-zhou/zcode/internal/zcode"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		in   string
		want zcode.Mode
	}{
		{"plan", zcode.ModePlan},
		{" PLAN ", zcode.ModePlan},
		{"build", zcode.ModeBuild},
		{"", zcode.ModeBuild},
		{"nope", zcode.ModeBuild},
	}
	for _, tc := range cases {
		if got := zcode.ParseMode(tc.in); got != tc.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestModeValid(t *testing.T) {
	if !zcode.ModePlan.Valid() || !zcode.ModeBuild.Valid() {
		t.Error("plan and build should be valid modes")
	}
	if zcode.Mode("bogus").Valid() {
		t.Error("unknown mode should be invalid")
	}
}

func TestSetModeIgnoresUnknown(t *testing.T) {
	a := zcode.New(&fakeClient{responses: []llm.Response{{Content: "x"}}}, tools.NewRegistry(), zcode.Options{})
	a.SetMode(zcode.ModePlan)
	if a.Mode != zcode.ModePlan {
		t.Fatalf("Mode = %q, want plan", a.Mode)
	}
	a.SetMode(zcode.Mode("bogus"))
	if a.Mode != zcode.ModePlan {
		t.Errorf("SetMode should ignore unknown modes, got %q", a.Mode)
	}
	a.SetMode(zcode.ModeBuild)
	if a.Mode != zcode.ModeBuild {
		t.Errorf("Mode = %q, want build", a.Mode)
	}
}
