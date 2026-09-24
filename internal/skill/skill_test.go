package skill

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "release.md", "---\nname: release\ndescription: 发布流程\n---\n1. 打 tag\n2. 构建")
	write(t, dir, "notes.md", "随便写点")
	write(t, dir, "empty.md", "---\ndescription: 空\n---\n")
	write(t, dir, "skip.txt", "x")

	skills, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("loaded %d skills, want 2: %+v", len(skills), skills)
	}
	// sorted: notes, release
	if skills[0].Name != "notes" || skills[1].Name != "release" {
		t.Errorf("order = %q, %q", skills[0].Name, skills[1].Name)
	}
	if skills[1].Description != "发布流程" || !strings.Contains(skills[1].Body, "打 tag") {
		t.Errorf("release = %+v", skills[1])
	}
}

func TestLoadFallsBackToFileName(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "my-skill.md", "正文")
	skills, err := Load(dir)
	if err != nil || len(skills) != 1 {
		t.Fatalf("Load = %+v, %v", skills, err)
	}
	if skills[0].Name != "my-skill" || skills[0].Body != "正文" {
		t.Errorf("skill = %+v", skills[0])
	}
}

func TestLoadMissingDir(t *testing.T) {
	skills, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(skills) != 0 {
		t.Errorf("Load = %+v, %v", skills, err)
	}
}
