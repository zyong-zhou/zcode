// Package tools provides the capabilities the agent can invoke and a Registry
// that keeps their order stable.
package tools

import (
	"context"
	"sync"
)

// Tool is a capability the agent can call.
type Tool interface {
	// Name is the stable identifier used by the model.
	Name() string
	// Description tells the model what the tool does.
	Description() string
	// Parameters is a JSON Schema object describing the arguments.
	Parameters() map[string]any
	// Run executes the tool with raw JSON arguments and returns text that is
	// fed back to the model.
	Run(ctx context.Context, args string) (string, error)
}

// PlanSafe is an optional interface implemented by tools that may run while the
// agent is in plan mode. Tools that can change the user's project must return
// false. Tools that do not implement it are treated as unsafe and are hidden in
// plan mode, so opting in is explicit.
type PlanSafe interface {
	PlanSafe() bool
}

// IsPlanSafe reports whether t may run in plan mode.
func IsPlanSafe(t Tool) bool {
	if p, ok := t.(PlanSafe); ok {
		return p.PlanSafe()
	}
	return false
}

// Previewer is an optional interface implemented by tools that can describe a
// call before it runs, so an approval prompt can show what will change. It
// returns ("", false) when there is nothing useful to show.
type Previewer interface {
	Preview(args string) (string, bool)
}

// Preview returns a human-readable summary of a pending call, or "" when the
// tool has no preview or its arguments cannot be parsed.
func Preview(t Tool, args string) string {
	if p, ok := t.(Previewer); ok {
		if s, ok := p.Preview(args); ok {
			return s
		}
	}
	return ""
}

// Registry is a name-indexed, order-stable set of tools. It is safe for
// concurrent use: MCP tool-list changes are applied from a server notification
// goroutine while the agent loop reads the registry, and an unsynchronised map
// would make that a fatal runtime error rather than a recoverable panic.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	names []string
}

// NewRegistry returns a registry containing the given tools.
func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range ts {
		r.Register(t)
	}
	return r
}

// Register adds or replaces a tool. Replacement keeps the original position.
func (r *Registry) Register(t Tool) {
	if t == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Name()
	if _, ok := r.tools[name]; !ok {
		r.names = append(r.names, name)
	}
	r.tools[name] = t
}

// Unregister removes the tool registered under name. It is a no-op when the
// name is unknown.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return
	}
	delete(r.tools, name)
	for i, n := range r.names {
		if n == name {
			r.names = append(r.names[:i], r.names[i+1:]...)
			break
		}
	}
}

// Get returns the tool registered under name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// All returns tools in registration order.
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.names))
	for _, n := range r.names {
		out = append(out, r.tools[n])
	}
	return out
}
