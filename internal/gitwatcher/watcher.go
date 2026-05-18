package gitwatcher

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/model"
)

type Watcher struct {
	mu              sync.RWMutex
	eventBus        EventEmitter
	watchRepos      map[string]*RepoState
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	pollInterval    time.Duration
	headFileWatcher FileWatcher
}

type RepoState struct {
	Path          string
	CurrentBranch string
	CurrentHead   string
	LastCommit    string
	IsRepo        bool
}

type EventEmitter interface {
	Emit(event agent.Event)
}

// FileWatcher interface for watching HEAD file
type FileWatcher interface {
	AddDirectory(path string) error
	RemoveDirectory(path string) error
}

// New creates a new git watcher
func New(eventBus EventEmitter) *Watcher {
	return &Watcher{
		eventBus:     eventBus,
		watchRepos:   make(map[string]*RepoState),
		pollInterval: 5 * time.Second,
	}
}

// Start begins watching git repositories
func (w *Watcher) Start(ctx context.Context, fileWatcher FileWatcher) error {
	w.ctx, w.cancel = context.WithCancel(ctx)
	w.headFileWatcher = fileWatcher

	// Start periodic poller
	w.wg.Add(1)
	go w.pollRepos()

	return nil
}

// AddRepository adds a git repository to watch
func (w *Watcher) AddRepository(repoPath string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.watchRepos[repoPath]; exists {
		return nil // Already watching
	}

	state := &RepoState{
		Path: repoPath,
	}

	// Check if it's a git repository and get initial state
	if err := w.updateRepoState(state); err != nil {
		return fmt.Errorf("failed to initialize repo state: %w", err)
	}

	if !state.IsRepo {
		return fmt.Errorf("not a git repository: %s", repoPath)
	}

	w.watchRepos[repoPath] = state

	// Watch the HEAD file for branch changes
	gitDir := filepath.Join(repoPath, ".git")
	if w.headFileWatcher != nil {
		if err := w.headFileWatcher.AddDirectory(gitDir); err != nil {
			logger.Warn("Failed to watch .git directory: %v", err)
		}
	}

	logger.Info("Started watching git repository: %s (branch: %s)", repoPath, state.CurrentBranch)

	// Emit initial branch event
	w.emitBranchEvent(state.CurrentBranch, "", state.CurrentBranch)

	return nil
}

// RemoveRepository removes a git repository from watching
func (w *Watcher) RemoveRepository(repoPath string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.watchRepos[repoPath]; !exists {
		return nil // Not watching
	}

	gitDir := filepath.Join(repoPath, ".git")
	if w.headFileWatcher != nil {
		if err := w.headFileWatcher.RemoveDirectory(gitDir); err != nil {
			logger.Warn("Failed to stop watching .git directory: %v", err)
		}
	}

	delete(w.watchRepos, repoPath)
	logger.Info("Stopped watching git repository: %s", repoPath)

	return nil
}

// updateRepoState updates the state of a git repository
func (w *Watcher) updateRepoState(state *RepoState) error {
	// Check if .git directory exists
	gitDir := filepath.Join(state.Path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		state.IsRepo = false
		return nil
	}

	state.IsRepo = true

	// Get current branch
	branch, err := w.runGitCommand(state.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("failed to get branch: %w", err)
	}
	state.CurrentBranch = strings.TrimSpace(branch)

	// Get current HEAD commit
	head, err := w.runGitCommand(state.Path, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}
	state.CurrentHead = strings.TrimSpace(head)

	// Get last commit info
	lastCommit, err := w.runGitCommand(state.Path, "log", "-1", "--format=%H %s")
	if err != nil {
		return fmt.Errorf("failed to get last commit: %w", err)
	}
	state.LastCommit = strings.TrimSpace(lastCommit)

	return nil
}

// pollRepos periodically polls git repositories for changes
func (w *Watcher) pollRepos() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			logger.Info("Git watcher stopped")
			return

		case <-ticker.C:
			w.checkAllRepos()
		}
	}
}

// checkAllRepos checks all watched repositories for changes
func (w *Watcher) checkAllRepos() {
	w.mu.Lock()
	defer w.mu.Unlock()

	for repoPath, state := range w.watchRepos {
		oldBranch := state.CurrentBranch
		oldHead := state.CurrentHead

		if err := w.updateRepoState(state); err != nil {
			logger.Error("Failed to update repo state for %s: %v", repoPath, err)
			continue
		}

		// Check for branch changes
		if state.CurrentBranch != oldBranch {
			logger.Info("Branch changed in %s: %s -> %s", repoPath, oldBranch, state.CurrentBranch)
			w.emitBranchEvent(state.CurrentBranch, oldBranch, state.CurrentBranch)
		}

		// Check for new commits
		if state.CurrentHead != oldHead {
			logger.Info("New commit in %s: %s", repoPath, state.CurrentHead)
			w.emitCommitEvent(state, oldHead)
		}

		// Check for status changes
		if w.hasStatusChanged(state.Path) {
			logger.Debug("Git status changed in %s", repoPath)
			w.emitStatusEvent(state)
		}
	}
}

// hasStatusChanged checks if git working directory status has changed
func (w *Watcher) hasStatusChanged(repoPath string) bool {
	status, err := w.runGitCommand(repoPath, "status", "--porcelain")
	if err != nil {
		return false
	}
	return strings.TrimSpace(status) != ""
}

// emitBranchEvent emits a branch change event
func (w *Watcher) emitBranchEvent(newBranch string, oldBranch string, currentHead string) {
	data := map[string]any{
		"new_branch":  newBranch,
		"old_branch":  oldBranch,
		"current_head": currentHead,
		"timestamp":    time.Now().Unix(),
	}

	w.eventBus.Emit(agent.Event{
		Type: model.EventTypeHostGitBranchChanged,
		Data: data,
	})

	logger.Debug("Git branch event emitted: %s -> %s", oldBranch, newBranch)
}

// emitCommitEvent emits a commit event
func (w *Watcher) emitCommitEvent(state *RepoState, oldHead string) {
	// Get commit details
	commitDetails, err := w.runGitCommand(state.Path, "log", "-1", "--format=%H|%an|%ae|%s|%ct")
	if err != nil {
		logger.Error("Failed to get commit details: %v", err)
		return
	}

	parts := strings.Split(commitDetails, "|")
	if len(parts) < 5 {
		logger.Error("Invalid commit details format")
		return
	}

	data := map[string]any{
		"hash":         parts[0],
		"author_name":  parts[1],
		"author_email": parts[2],
		"message":      parts[3],
		"timestamp":    parts[4],
		"branch":       state.CurrentBranch,
		"repo_path":    state.Path,
		"emitted_at":   time.Now().Unix(),
	}

	w.eventBus.Emit(agent.Event{
		Type: model.EventTypeHostGitCommit,
		Data: data,
	})

	logger.Debug("Git commit event emitted: %s in %s", parts[0], state.Path)
}

// emitStatusEvent emits a status change event
func (w *Watcher) emitStatusEvent(state *RepoState) {
	status, err := w.runGitCommand(state.Path, "status", "--porcelain")
	if err != nil {
		return
	}

	// Parse status to get changed files
	changedFiles := strings.Split(strings.TrimSpace(status), "\n")

	data := map[string]any{
		"branch":       state.CurrentBranch,
		"changed_files": changedFiles,
		"repo_path":    state.Path,
		"timestamp":    time.Now().Unix(),
	}

	w.eventBus.Emit(agent.Event{
		Type: model.EventTypeHostGitStatusChanged,
		Data: data,
	})

	logger.Debug("Git status event emitted: %d changed files in %s", len(changedFiles), state.Path)
}

// runGitCommand executes a git command in the specified directory
func (w *Watcher) runGitCommand(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", err
	}

	// Read stdout
	var stdoutBuf strings.Builder
	stdoutScanner := bufio.NewScanner(stdout)
	for stdoutScanner.Scan() {
		stdoutBuf.WriteString(stdoutScanner.Text())
		stdoutBuf.WriteString("\n")
	}

	// Read stderr (for error handling)
	var stderrBuf strings.Builder
	stderrScanner := bufio.NewScanner(stderr)
	for stderrScanner.Scan() {
		stderrBuf.WriteString(stderrScanner.Text())
		stderrBuf.WriteString("\n")
	}

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("git command failed: %w, stderr: %s", err, stderrBuf.String())
	}

	return stdoutBuf.String(), nil
}

// Stop stops the git watcher
func (w *Watcher) Stop() {
	if w == nil {
		return
	}

	if w.cancel != nil {
		w.cancel()
	}

	w.wg.Wait()
	logger.Info("Git watcher stopped")
}

// WatchingRepositories returns list of repositories being watched
func (w *Watcher) WatchingRepositories() []string {
	if w == nil {
		return nil
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	repos := make([]string, 0, len(w.watchRepos))
	for repo := range w.watchRepos {
		repos = append(repos, repo)
	}
	return repos
}

// GetRepositoryState returns the current state of a watched repository
func (w *Watcher) GetRepositoryState(repoPath string) (*RepoState, error) {
	if w == nil {
		return nil, fmt.Errorf("watcher not initialized")
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	state, exists := w.watchRepos[repoPath]
	if !exists {
		return nil, fmt.Errorf("repository not being watched: %s", repoPath)
	}

	// Return a copy to avoid race conditions
	stateCopy := *state
	return &stateCopy, nil
}
