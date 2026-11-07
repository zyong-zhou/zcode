package tools

import "strings"

// Hard caps on traversal size. The model supplies max_entries and max_results,
// so without an upper bound one call could ask for unbounded memory.
const (
	maxListEntries   = 5000
	maxListDepth     = 32
	maxSearchResults = 1000
)

// ignoredDirs are directory names skipped by list_dir and search so their
// output stays focused. Agent-visible file tools should reuse isIgnoredDir so
// traversal behavior stays consistent.
var ignoredDirs = map[string]bool{
	".git":         true,
	".hg":          true,
	".svn":         true,
	".idea":        true,
	".vscode":      true,
	"node_modules": true,
	"vendor":       true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
}

// isIgnoredDir matches case-insensitively, because Windows checkouts routinely
// carry names such as ".Git" or "Node_Modules".
func isIgnoredDir(name string) bool { return ignoredDirs[strings.ToLower(name)] }
