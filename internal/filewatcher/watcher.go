package filewatcher

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/model"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	mu        sync.RWMutex
	watcher   *fsnotify.Watcher
	eventBus  EventEmitter
	watchDirs map[string]bool
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
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

// AddDirectory adds a directory to watch
func (w *Watcher) AddDirectory(dir string) error {
	if w == nil || w.watcher == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.watchDirs[dir] {
		return nil // Already watching
	}

	err := w.watcher.Add(dir)
	if err != nil {
		logger.Error("Failed to watch directory %s: %v", dir, err)
		return err
	}

	w.watchDirs[dir] = true
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
		return nil // Not watching
	}

	err := w.watcher.Remove(dir)
	if err != nil {
		logger.Error("Failed to stop watching directory %s: %v", dir, err)
		return err
	}

	delete(w.watchDirs, dir)
	logger.Info("Stopped watching directory: %s", dir)
	return nil
}

// processEvents processes file system events
func (w *Watcher) processEvents() {
	defer w.wg.Done()

	debounceTimer := time.NewTimer(0)
	<-debounceTimer.C // Stop the initial timer

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

			// Filter out temporary files and hidden files
			if w.shouldIgnore(event.Name) {
				continue
			}

			// Debounce events
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

			// Process debounced events
			for _, event := range eventsToProcess {
				w.emitEvent(event)
			}
		}
	}
}

// shouldIgnore determines if a file should be ignored
func (w *Watcher) shouldIgnore(path string) bool {
	base := filepath.Base(path)

	// Ignore temporary files
	ignorePatterns := []string{
		".",     // Hidden files
		"~",     // Backup files
		".tmp",  // Temp files
		".swp",  // Swap files
		".bak",  // Backup files
		"node_modules/",
		".git/",
	}

	for _, pattern := range ignorePatterns {
		if base == pattern || filepath.Ext(path) == pattern {
			return true
		}
	}

	return false
}

// emitEvent emits an event for the file system change
func (w *Watcher) emitEvent(event fsnotify.Event) {
	var eventType string
	var data = map[string]any{
		"file":      event.Name,
		"timestamp": time.Now().Unix(),
	}

	// Determine event type
	if event.Op&fsnotify.Create == fsnotify.Create {
		eventType = model.EventTypeHostFileCreated
	} else if event.Op&fsnotify.Write == fsnotify.Write {
		eventType = model.EventTypeHostFileUpdated
	} else if event.Op&fsnotify.Remove == fsnotify.Remove {
		eventType = model.EventTypeHostFileDeleted
	} else if event.Op&fsnotify.Rename == fsnotify.Rename {
		eventType = model.EventTypeHostFileRenamed
	} else if event.Op&fsnotify.Chmod == fsnotify.Chmod {
		// Ignore permission changes
		return
	} else {
		return // Unknown operation
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
