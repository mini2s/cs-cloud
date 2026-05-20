package filewatcher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShouldIgnore_HiddenFiles(t *testing.T) {
	w := &Watcher{ignore: newGitignoreMatcher()}

	tests := []struct {
		path     string
		expected bool
	}{
		{".hidden", true},
		{".env", true},
		{".gitignore", false}, // special: we allow tracking .gitignore
		{".git/config", true},
		{"normal.txt", false},
		{"src/main.go", false},
	}

	for _, tt := range tests {
		got := w.shouldIgnore(tt.path)
		if got != tt.expected {
			t.Errorf("shouldIgnore(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestShouldIgnore_BuiltinDirs(t *testing.T) {
	w := &Watcher{ignore: newGitignoreMatcher()}

	tests := []struct {
		path     string
		expected bool
	}{
		{"project/node_modules/foo.js", true},
		{"project/.git/HEAD", true},
		{"project/build/output.js", true},
		{"project/dist/bundle.js", true},
		{"project/vendor/pkg/mod/cache", true},
		{"project/.idea/workspace.xml", true},
		{"project/.vscode/settings.json", true},
		{"project/target/classes/App.class", true},
		{"project/__pycache__/module.pyc", true},
		{"project/.next/static/chunks/main.js", true},
		{"project/src/App.tsx", false},
		{"project/lib/util.go", false},
	}

	for _, tt := range tests {
		got := w.shouldIgnore(tt.path)
		if got != tt.expected {
			t.Errorf("shouldIgnore(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestShouldIgnore_TempSuffixes(t *testing.T) {
	w := &Watcher{ignore: newGitignoreMatcher()}

	tests := []struct {
		path     string
		expected bool
	}{
		{"file.tmp", true},
		{"file.swp", true},
		{"file.swo", true},
		{"file.bak", true},
		{"backup~", true},
		{"src/file.tmp", true},
		{"normal.go", false},
		{"tmp.go", false}, // "tmp" is not a suffix match
	}

	for _, tt := range tests {
		got := w.shouldIgnore(tt.path)
		if got != tt.expected {
			t.Errorf("shouldIgnore(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestGitignoreMatcher_BasicPatterns(t *testing.T) {
	g := newGitignoreMatcher()
	workspace := "/project"

	g.rules[workspace] = []gitignoreRule{
		{pattern: "*.log", negated: false},
		{pattern: "build", negated: false},
		{pattern: "secret.keys", negated: false},
	}

	tests := []struct {
		path     string
		expected bool
	}{
		{"/project/app.log", true},
		{"/project/logs/error.log", true},
		{"/project/build/output.js", true},
		{"/project/src/build/helper.go", true},
		{"/project/secret.keys", true},
		{"/project/src/main.go", false},
		{"/project/app.ts", false},
	}

	for _, tt := range tests {
		got := g.match(tt.path)
		if got != tt.expected {
			t.Errorf("match(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestGitignoreMatcher_Negation(t *testing.T) {
	g := newGitignoreMatcher()
	workspace := "/project"

	g.rules[workspace] = []gitignoreRule{
		{pattern: "*.log", negated: false},
		{pattern: "important.log", negated: true},
	}

	if !g.match("/project/debug.log") {
		t.Error("expected debug.log to be ignored")
	}
	if g.match("/project/important.log") {
		t.Error("expected important.log to be un-ignored (negation)")
	}
}

func TestGitignoreMatcher_RootAnchored(t *testing.T) {
	g := newGitignoreMatcher()
	workspace := "/project"

	g.rules[workspace] = []gitignoreRule{
		{pattern: "TODO", rootOnly: true},
	}

	tests := []struct {
		path     string
		expected bool
	}{
		{"/project/TODO", true},
		{"/project/src/TODO", false}, // rootOnly: only matches at workspace root
	}

	for _, tt := range tests {
		got := g.match(tt.path)
		if got != tt.expected {
			t.Errorf("match(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestGitignoreMatcher_Doublestar(t *testing.T) {
	g := newGitignoreMatcher()
	workspace := "/project"

	g.rules[workspace] = []gitignoreRule{
		{pattern: "logs/**/debug.log"},
	}

	tests := []struct {
		path     string
		expected bool
	}{
		{"/project/logs/debug.log", true},
		{"/project/logs/2024/debug.log", true},
		{"/project/logs/2024/01/debug.log", true},
		{"/project/src/debug.log", false},
	}

	for _, tt := range tests {
		got := g.match(tt.path)
		if got != tt.expected {
			t.Errorf("match(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestGitignoreMatcher_LoadFromWorkspace(t *testing.T) {
	// Create a temp workspace with a .gitignore file
	workspace, err := os.MkdirTemp("", "gitignore_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	gitignoreContent := `# Build output
dist/
build/

# Logs
*.log

# Environment
.env
!important.env

# IDE
.idea/

# OS
Thumbs.db
`
	err = os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte(gitignoreContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	g := newGitignoreMatcher()
	g.loadFromWorkspace(workspace)

	// Verify rules were loaded
	g.mu.RLock()
	rules := g.rules[workspace]
	g.mu.RUnlock()

	if len(rules) == 0 {
		t.Fatal("expected rules to be loaded from .gitignore")
	}

	// Test effective matching
	tests := []struct {
		path     string
		expected bool
	}{
		{filepath.Join(workspace, "app.log"), true},
		{filepath.Join(workspace, "important.env"), false}, // negation
		{filepath.Join(workspace, "Thumbs.db"), true},
		{filepath.Join(workspace, "main.go"), false},
	}

	for _, tt := range tests {
		got := g.match(tt.path)
		if got != tt.expected {
			t.Errorf("match(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestGitignoreMatcher_RemoveWorkspace(t *testing.T) {
	g := newGitignoreMatcher()
	workspace := "/project"

	g.rules[workspace] = []gitignoreRule{
		{pattern: "*.log"},
	}

	got := g.match("/project/app.log")
	if !got {
		t.Error("expected match before removal")
	}

	g.removeWorkspace(workspace)

	g.mu.RLock()
	_, exists := g.rules[workspace]
	g.mu.RUnlock()
	if exists {
		t.Error("expected workspace rules to be removed")
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		expected bool
	}{
		{"foo.go", "*.go", true},
		{"foo.ts", "*.go", false},
		{"main.go", "main.go", true},
		{"main.go", "?ain.go", true},           // ? matches 'm'
		{"main.go", "?.ain.go", false},          // length mismatch (7 vs 8)
		{"readme", "README", false}, // case-sensitive
		{"any", "*", true},
	}

	for _, tt := range tests {
		got := globMatch(tt.name, tt.pattern)
		if got != tt.expected {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tt.name, tt.pattern, got, tt.expected)
		}
	}
}

func TestShouldIgnore_NoWatcherDirLeak(t *testing.T) {
	// Ensure builtin dirs in the middle of a path are caught
	w := &Watcher{ignore: newGitignoreMatcher()}

	tests := []struct {
		path     string
		expected bool
	}{
		// node_modules anywhere in the path
		{"project/node_modules/react/index.js", true},
		{"project/apps/web/node_modules/lodash.js", true},
		// .git anywhere in the path
		{"project/.git/objects/pack/abc", true},
		// Normal paths should not match
		{"project/src/node_modules_utils.go", false},
	}

	for _, tt := range tests {
		got := w.shouldIgnore(tt.path)
		if got != tt.expected {
			t.Errorf("shouldIgnore(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}
