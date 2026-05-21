package filewatcher

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/model"

	"github.com/fsnotify/fsnotify"
)

// builtinIgnoreDirs are always ignored regardless of .gitignore.
var builtinIgnoreDirs = []string{
	".git",
	".hg",
	".svn",
	"node_modules",
	"__pycache__",
	".cache",
	".cache_large",
	"dist",
	"build",
	".next",
	".nuxt",
	".turbo",
	"target",
	"vendor",
	".idea",
	".vscode",
	".DS_Store",
}

// builtinIgnoreSuffixes are file suffixes always ignored.
var builtinIgnoreSuffixes = []string{
	".tmp",
	".swp",
	".swo",
	".bak",
	"~",
}

type Watcher struct {
	mu             sync.RWMutex
	watcher        *fsnotify.Watcher
	eventBus       EventEmitter
	watchDirs      map[string]bool
	ignore         *gitignoreMatcher // merged .gitignore rules keyed by watch dir
	useGitignore   bool              // whether to load and apply .gitignore rules
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

type EventEmitter interface {
	Emit(event agent.Event)
}

// New creates a new file watcher
func New(eventBus EventEmitter) *Watcher {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Error("Failed to create file watcher: %v", err)
		return nil
	}

	return &Watcher{
		watcher:   watcher,
		eventBus:  eventBus,
		watchDirs: make(map[string]bool),
		ignore:    newGitignoreMatcher(),
	}
}

// Start begins watching directories
func (w *Watcher) Start(ctx context.Context) error {
	if w == nil || w.watcher == nil {
		return nil
	}

	w.ctx, w.cancel = context.WithCancel(ctx)

	w.wg.Add(1)
	go w.processEvents()

	return nil
}

// AddDirectory adds a directory to watch and loads its .gitignore rules.
func (w *Watcher) AddDirectory(dir string) error {
	if w == nil || w.watcher == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.watchDirs[dir] {
		return nil
	}

	err := w.watcher.Add(dir)
	if err != nil {
		logger.Error("Failed to watch directory %s: %v", dir, err)
		return err
	}

	w.watchDirs[dir] = true

	// Load .gitignore from the workspace root (disabled by default)
	if w.useGitignore {
		w.ignore.loadFromWorkspace(dir)
	}

	logger.Info("Started watching directory: %s", dir)
	return nil
}

// RemoveDirectory removes a directory from watching
func (w *Watcher) RemoveDirectory(dir string) error {
	if w == nil || w.watcher == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.watchDirs[dir] {
		return nil
	}

	err := w.watcher.Remove(dir)
	if err != nil {
		logger.Error("Failed to stop watching directory %s: %v", dir, err)
		return err
	}

	delete(w.watchDirs, dir)
	w.ignore.removeWorkspace(dir)
	logger.Info("Stopped watching directory: %s", dir)
	return nil
}

// processEvents processes file system events
func (w *Watcher) processEvents() {
	defer w.wg.Done()

	debounceTimer := time.NewTimer(0)
	<-debounceTimer.C // drain the initial tick

	debounceEvents := make(map[string]fsnotify.Event)
	var debounceMu sync.Mutex

	for {
		select {
		case <-w.ctx.Done():
			logger.Info("File watcher stopped")
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if w.shouldIgnore(event.Name) {
				continue
			}

			debounceMu.Lock()
			debounceEvents[event.Name] = event
			debounceMu.Unlock()

			debounceTimer.Reset(100 * time.Millisecond)

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			logger.Error("File watcher error: %v", err)

		case <-debounceTimer.C:
			debounceMu.Lock()
			eventsToProcess := debounceEvents
			debounceEvents = make(map[string]fsnotify.Event)
			debounceMu.Unlock()

			for _, event := range eventsToProcess {
				w.emitEvent(event)
			}
		}
	}
}

// shouldIgnore determines if a file path should be ignored based on:
//  1. built-in blacklist (hidden files, common temp suffixes, well-known dirs)
//  2. .gitignore rules loaded from the workspace
func (w *Watcher) shouldIgnore(path string) bool {
	base := filepath.Base(path)

	// Hidden files/dirs (names starting with ".")
	if strings.HasPrefix(base, ".") && base != "." {
		// Allow .gitignore itself to be tracked for reloading,
		// but ignore other hidden entries
		if base != ".gitignore" {
			return true
		}
		return false
	}

	// Backup files ending with ~
	if strings.HasSuffix(base, "~") {
		return true
	}

	// Temp file suffixes
	for _, sfx := range builtinIgnoreSuffixes {
		if sfx != "~" && strings.HasSuffix(base, sfx) {
			return true
		}
	}

	// Check if any path component matches a builtin ignore dir
	for part := path; part != ""; {
		dir := filepath.Base(part)
		for _, ignore := range builtinIgnoreDirs {
			if dir == ignore {
				return true
			}
		}
		parent := filepath.Dir(part)
		if parent == part {
			break
		}
		part = parent
	}

	// Check .gitignore rules (disabled by default)
	if w.useGitignore && w.ignore != nil && w.ignore.match(path) {
		return true
	}

	return false
}

// emitEvent emits an event for the file system change
func (w *Watcher) emitEvent(event fsnotify.Event) {
	var eventType string
	data := map[string]any{
		"file":      event.Name,
		"timestamp": time.Now().Unix(),
	}

	if event.Op&fsnotify.Create == fsnotify.Create {
		eventType = model.EventTypeHostFileCreated
	} else if event.Op&fsnotify.Write == fsnotify.Write {
		eventType = model.EventTypeHostFileUpdated
	} else if event.Op&fsnotify.Remove == fsnotify.Remove {
		eventType = model.EventTypeHostFileDeleted
	} else if event.Op&fsnotify.Rename == fsnotify.Rename {
		eventType = model.EventTypeHostFileRenamed
	} else if event.Op&fsnotify.Chmod == fsnotify.Chmod {
		return
	} else {
		return
	}

	w.eventBus.Emit(agent.Event{
		Type: eventType,
		Data: data,
	})

	logger.Debug("File event emitted: %s for %s", eventType, event.Name)
}

// Stop stops the file watcher
func (w *Watcher) Stop() {
	if w == nil {
		return
	}
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
	if w.watcher != nil {
		w.watcher.Close()
	}
	logger.Info("File watcher stopped")
}

// WatchingDirectories returns list of directories being watched
func (w *Watcher) WatchingDirectories() []string {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()

	dirs := make([]string, 0, len(w.watchDirs))
	for dir := range w.watchDirs {
		dirs = append(dirs, dir)
	}
	return dirs
}

// ---------------------------------------------------------------------------
// Lightweight .gitignore matcher
// ---------------------------------------------------------------------------

// gitignoreMatcher holds parsed .gitignore rules for one or more workspaces.
type gitignoreMatcher struct {
	mu    sync.RWMutex
	rules map[string][]gitignoreRule // workspaceRoot -> rules
}

type gitignoreRule struct {
	pattern   string
	negated   bool
	dirOnly   bool
	rootOnly  bool // pattern starts with "/" — anchored to workspace root
}

func newGitignoreMatcher() *gitignoreMatcher {
	return &gitignoreMatcher{
		rules: make(map[string][]gitignoreRule),
	}
}

// loadFromWorkspace reads and parses .gitignore from the given workspace root.
func (g *gitignoreMatcher) loadFromWorkspace(workspace string) {
	gitignorePath := filepath.Join(workspace, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		// No .gitignore — that's fine
		return
	}

	var rules []gitignoreRule
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules = append(rules, parseGitignoreLine(line))
	}

	g.mu.Lock()
	g.rules[workspace] = rules
	g.mu.Unlock()

	logger.Info("Loaded %d .gitignore rules from %s", len(rules), workspace)
}

// removeWorkspace removes rules for a workspace that is no longer watched.
func (g *gitignoreMatcher) removeWorkspace(workspace string) {
	g.mu.Lock()
	delete(g.rules, workspace)
	g.mu.Unlock()
}

// match checks if the given path is ignored by any loaded .gitignore rules.
// It iterates all workspaces and checks if the path falls under one of them.
func (g *gitignoreMatcher) match(path string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Clean and normalize
	path = filepath.ToSlash(filepath.Clean(path))

	for workspace, rules := range g.rules {
		ws := filepath.ToSlash(filepath.Clean(workspace))
		if !strings.HasPrefix(path, ws+"/") && path != ws {
			continue
		}
		// Compute relative path
		rel := strings.TrimPrefix(path, ws+"/")

		ignored := false
		for _, rule := range rules {
			if rule.dirOnly {
				// dirOnly rules only apply to directories — we can't tell from
				// the path alone, so we check if it could be a dir segment.
				// For simplicity, skip dirOnly for file events.
				continue
			}
			if matchGitignorePattern(rel, rule) {
				if rule.negated {
					ignored = false
				} else {
					ignored = true
				}
			}
		}
		if ignored {
			return true
		}
	}
	return false
}

// parseGitignoreLine converts a single .gitignore line into a rule.
func parseGitignoreLine(line string) gitignoreRule {
	r := gitignoreRule{}

	// Negation
	if strings.HasPrefix(line, "!") {
		r.negated = true
		line = line[1:]
	}

	// Directory-only trailing slash
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}

	// Root-anchored
	if strings.HasPrefix(line, "/") {
		r.rootOnly = true
		line = line[1:]
	}

	r.pattern = line
	return r
}

// matchGitignorePattern performs glob-style matching of a relative path
// against a single gitignore rule.
func matchGitignorePattern(relPath string, rule gitignoreRule) bool {
	pattern := rule.pattern

	// If rootOnly, the pattern must match from the beginning of relPath
	if rule.rootOnly {
		return globMatch(relPath, pattern)
	}

	// Full path match first
	if globMatch(relPath, pattern) {
		return true
	}

	// For patterns without / (simple name patterns like "build", "*.log"),
	// match against any single path component
	if !strings.Contains(pattern, "/") {
		parts := strings.Split(relPath, "/")
		for _, part := range parts {
			if globMatch(part, pattern) {
				return true
			}
		}
		return false
	}

	// For patterns with / (but no **), try matching from each directory level
	parts := strings.Split(relPath, "/")
	for i := 0; i < len(parts); i++ {
		sub := strings.Join(parts[i:], "/")
		if globMatch(sub, pattern) {
			return true
		}
	}
	return false
}

// globMatch performs glob matching with *, ?, and ** support.
// ** matches zero or more path segments (i.e. any characters including /).
func globMatch(name, pattern string) bool {
	return globMatchRecursive(name, pattern, false)
}

// globMatchRecursive is the core matcher. allowSlash controls whether *
// can match /. For ** expansion it can; otherwise it cannot.
func globMatchRecursive(name, pattern string, allowSlash bool) bool {
	for {
		if pattern == "" {
			return name == ""
		}

		// Check for **
		if len(pattern) >= 2 && pattern[0] == '*' && pattern[1] == '*' {
			// Collapse ***+ to **
			pattern = strings.TrimLeft(pattern, "*")
			pattern = "**" + pattern

			// ** matches everything — if rest of pattern is empty, done
			if len(pattern) == 2 {
				return true
			}

			// Try matching rest of pattern at every position in name
			rest := pattern[2:]
			// Skip leading / in rest after **
			if strings.HasPrefix(rest, "/") {
				rest = rest[1:]
			}
			for i := 0; i <= len(name); i++ {
				if globMatchRecursive(name[i:], rest, true) {
					return true
				}
			}
			return false
		}

		if name == "" {
			// Remaining pattern must be all stars
			return strings.Trim(pattern, "*") == ""
		}

		pc := pattern[0]
		if pc == '?' {
			// ? matches any single character (including / in our context)
			name = name[1:]
			pattern = pattern[1:]
			continue
		}

		if pc == '*' {
			// Single * — matches any characters except /
			rest := pattern[1:]
			for i := 0; i <= len(name); i++ {
				if allowSlash || i == 0 || name[i-1] != '/' {
					// Try matching rest from position i
					if i < len(name) && !allowSlash && name[i] == '/' {
						continue
					}
				}
				if globMatchRecursive(name[i:], rest, allowSlash) {
					return true
				}
				if !allowSlash && i < len(name) && name[i] == '/' {
					break
				}
			}
			return false
		}

		// Literal character
		if name[0] == pc {
			name = name[1:]
			pattern = pattern[1:]
			continue
		}

		return false
	}
}
