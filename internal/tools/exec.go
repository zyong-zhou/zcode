package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RunCommand executes an executable with arguments inside Dir. It never invokes
// a shell, so arguments are not subject to shell interpolation.
type RunCommand struct {
	Dir string
	// Timeout bounds a single invocation. Defaults to 60s when <= 0.
	Timeout time.Duration
	// MaxOutputBytes caps returned output. Defaults to 64 KiB when <= 0.
	MaxOutputBytes int
}

func (t RunCommand) Name() string { return "run_command" }

// PlanSafe reports that running commands is not allowed in plan mode.
func (t RunCommand) PlanSafe() bool { return false }

func (t RunCommand) Description() string {
	return "Run an executable with arguments inside the project root and return its combined stdout/stderr."
}

func (t RunCommand) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "Executable to run, for example \"go\"."},
			"args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Arguments passed to the executable.",
			},
		},
		"required": []string{"command"},
	}
}

// Preview summarises the command line for an approval prompt.
func (t RunCommand) Preview(args string) (string, bool) {
	var in struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil || strings.TrimSpace(in.Command) == "" {
		return "", false
	}
	line := in.Command
	if len(in.Args) > 0 {
		line += " " + strings.Join(in.Args, " ")
	}
	return "执行 " + oneLine(line, previewMaxWidth), true
}

func (t RunCommand) Run(ctx context.Context, args string) (string, error) {
	var in struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("run_command: bad arguments: %w", err)
	}
	if in.Command == "" {
		return "", fmt.Errorf("run_command: command is required")
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, in.Command, in.Args...)
	if t.Dir != "" {
		cmd.Dir = t.Dir
	}
	out, err := cmd.CombinedOutput()
	result := truncate(string(out), t.MaxOutputBytes)
	if ctx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("run_command: timed out after %s", timeout)
	}
	if err != nil {
		return result, fmt.Errorf("run_command: %w%s", err, commandHint(in.Command, err))
	}
	return result, nil
}

// commandHint returns a short guidance suffix appended to a failed run_command
// result, steering the model back to the built-in tools when it reaches for a
// shell command that cannot work here (no shell, file commands not installed).
func commandHint(command string, err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Sprintf("。提示：%q 不可用；读写文件用 read_file/write_file/edit，列目录用 list_dir，检索用 search", command)
	}
	return "。提示：run_command 不经过 shell；若这是文件读写操作，请改用 read_file/write_file/edit/list_dir/search"
}
