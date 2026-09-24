package tools

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

func isIgnoredDir(name string) bool { return ignoredDirs[name] }
