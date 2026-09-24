package command

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndFrontMatter(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "design.md", "---\ndescription: 生成设计文档\n---\n请针对 $ARGUMENTS 输出设计。")
	write(t, dir, "notes.md", "记录：$1 / $2")
	write(t, dir, "ignore.txt", "nope")

	cmds, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("loaded %d commands, want 2: %+v", len(cmds), cmds)
	}
	if got := cmds["design"].Description; got != "生成设计文档" {
		t.Errorf("description = %q", got)
	}
	if got := cmds["design"].Expand("登录模块"); got != "请针对 登录模块 输出设计。" {
		t.Errorf("Expand = %q", got)
	}
	if got := cmds["notes"].Expand("甲 乙"); got != "记录：甲 / 乙" {
		t.Errorf("positional Expand = %q", got)
	}
}

func TestExpandAppendsWhenNoPlaceholder(t *testing.T) {
	cmd := Command{Body: "固定提示"}
	if got := cmd.Expand("附加要求"); got != "固定提示\n\n附加要求" {
		t.Errorf("Expand = %q", got)
	}
	if got := cmd.Expand(""); got != "固定提示" {
		t.Errorf("Expand(empty) = %q", got)
	}
}

func TestLoadMissingDir(t *testing.T) {
	cmds, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cmds) != 0 {
		t.Errorf("expected no commands, got %+v", cmds)
	}
}
