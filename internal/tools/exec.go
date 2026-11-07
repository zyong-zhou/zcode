package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// hardExecCaptureLimit bounds how much command output is buffered in memory,
// independent of MaxOutputBytes (which only caps what is handed to the model).
// Without it a runaway process (think `yes`) would be buffered in full and
// exhaust memory before truncation ever ran.
const hardExecCaptureLimit = 8 << 20 // 8 MiB

// waitDelay bounds how long Wait keeps waiting for a command's output pipes
// after the process itself has exited or been killed. A command that leaves a
// grandchild holding the inherited handles would otherwise keep the call
// blocked past its deadline.
const waitDelay = 5 * time.Second

// cappedWriter buffers at most limit bytes, keeping the head and the tail and
// discarding the middle, so total memory stays bounded while the diagnostically
// useful parts (the start, and the error at the end) survive. It is safe for
// concurrent use because os/exec may copy stdout and stderr on separate
// goroutines.
type cappedWriter struct {
	mu    sync.Mutex
	head  []byte
	tail  []byte
	limit int
	total int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	limit := w.limit
	if limit <= 0 {
		limit = hardExecCaptureLimit
	}
	half := limit / 2

	n := len(p)
	w.total += n

	if room := half - len(w.head); room > 0 {
		take := room
		if take > len(p) {
			take = len(p)
		}
		w.head = append(w.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		w.tail = append(w.tail, p...)
		if len(w.tail) > half {
			drop := len(w.tail) - half
			w.tail = append(w.tail[:0], w.tail[drop:]...)
		}
	}
	return n, nil
}

// String returns the captured output, with a marker when bytes were dropped.
func (w *cappedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	out := string(w.head) + string(w.tail)
	if omitted := w.total - len(w.head) - len(w.tail); omitted > 0 {
		out = string(w.head) +
			fmt.Sprintf("\n... [%d bytes omitted] ...\n", omitted) +
			string(w.tail)
	}
	return out
}

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
	return "Run an executable with arguments and return its combined stdout/stderr. The executable is invoked directly (no shell) with the project root as its working directory; it is not sandboxed and may touch paths outside the project."
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

	capture := &cappedWriter{limit: hardExecCaptureLimit}
	cmd := exec.CommandContext(ctx, in.Command, in.Args...)
	if t.Dir != "" {
		cmd.Dir = t.Dir
	}
	// Both streams share one writer so their interleaving is preserved.
	cmd.Stdout = capture
	cmd.Stderr = capture
	cmd.WaitDelay = waitDelay

	err := cmd.Run()
	result := truncate(capture.String(), t.MaxOutputBytes)

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return result, fmt.Errorf("run_command: timed out after %s", timeout)
	case errors.Is(err, exec.ErrWaitDelay):
		// The command itself finished; a surviving child kept the output pipes
		// open past waitDelay. Report what we captured rather than failing.
		return result, nil
	case err != nil:
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
